package controllers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"signalone/cmd/config"
	_ "signalone/docs"
	"signalone/pkg/models"
	"signalone/pkg/utils"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type LogAnalysisPayload struct {
	UserId        string `json:"userId"`
	ContainerName string `json:"containerName"`
	Logs          string `json:"logs"`
}

type GetIssuesPayload struct {
	UserId string `json:"userId"`
}

type MainController struct {
	issuesCollection        *mongo.Collection
	usersCollection         *mongo.Collection
	analysisStoreCollection *mongo.Collection
}

const ACCESS_TOKEN_EXPIRATION_TIME = time.Minute * 10
const REFRESH_TOKEN_EXPIRATION_TIME = time.Hour * 24

func NewMainController(issuesCollection *mongo.Collection,
	usersCollection *mongo.Collection,
	analysisStoreCollection *mongo.Collection) *MainController {
	return &MainController{
		issuesCollection:        issuesCollection,
		usersCollection:         usersCollection,
		analysisStoreCollection: analysisStoreCollection,
	}
}

// LogAnalysisTask godoc
// @Summary Perform log analysis and generate solutions.
// @Description Perform log analysis based on the provided logs and generate solutions.
// @Tags analysis
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer <token>"
// @Param logAnalysisPayload body LogAnalysisPayload true "Log analysis payload"
// @Success 200 {object} map[string]any
// @Failure 400 {object} map[string]any
// @Failure 401 {object} map[string]any
// @Router /issues/analysis [put]
func (c *MainController) LogAnalysisTask(ctx *gin.Context) {
	var user models.User
	var analysisResponse models.IssueAnalysis

	bearerToken := ctx.GetHeader("Authorization")
	if bearerToken == "" {
		ctx.JSON(401, gin.H{
			"message": "Unauthorized",
		})
		return
	}
	bearerToken = strings.TrimPrefix(bearerToken, "Bearer ")
	var logAnalysisPayload LogAnalysisPayload
	if err := ctx.ShouldBindJSON(&logAnalysisPayload); err != nil {
		ctx.JSON(400, gin.H{"error": err.Error()})
		return
	}
	userResult := c.usersCollection.FindOne(ctx, bson.M{"userId": logAnalysisPayload.UserId})
	err := userResult.Decode(&user)
	if err != nil {
		ctx.JSON(400, gin.H{"error": err.Error()})
		return
	}
	issueId := uuid.New().String()
	data := map[string]string{"logs": logAnalysisPayload.Logs}
	jsonData, _ := json.Marshal(data)
	analysisResponse, err = utils.CallPredictionAgentService(jsonData)
	if err != nil {
		ctx.JSON(500, gin.H{"error": err.Error()})
		return
	}

	if !user.IsPro {
		c.analysisStoreCollection.InsertOne(ctx, models.SavedAnalysis{
			Logs:       logAnalysisPayload.Logs,
			LogSummary: analysisResponse.LogSummary,
		})
	}

	formattedAnalysisLogs := strings.Split(logAnalysisPayload.Logs, "\n")

	c.issuesCollection.InsertOne(ctx, models.Issue{
		Id:                        issueId,
		UserId:                    logAnalysisPayload.UserId,
		ContainerName:             logAnalysisPayload.ContainerName,
		Score:                     0,
		Severity:                  strings.ToUpper("Critical"), // TODO: Implement severity detection
		Title:                     analysisResponse.Title,
		TimeStamp:                 time.Now(),
		IsResolved:                false,
		Logs:                      formattedAnalysisLogs,
		LogSummary:                analysisResponse.LogSummary,
		PredictedSolutionsSummary: analysisResponse.PredictedSolutions,
		PredictedSolutionsSources: analysisResponse.Sources,
	})
	ctx.JSON(200, gin.H{
		"message": "Success",
		"issueId": issueId,
	})
}

// IssuesSearch godoc
// @Summary Search for issues based on specified criteria.
// @Description Search for issues based on specified criteria.
// @Tags issues
// @Accept json
// @Produce json
// @Param offset query int false "Offset for paginated results"
// @Param limit query int false "Maximum number of results per page (default: 30, max: 100)"
// @Param searchString query string false "Search string for filtering issues"
// @Param container query string false "Filter by container name"
// @Param issueSeverity query string false "Filter by issue severity"
// @Param issueType query string false "Filter by issue type"
// @Param startTimestamp query string false "Filter issues starting from this timestamp (RFC3339 format)"
// @Param endTimestamp query string false "Filter issues until this timestamp (RFC3339 format)"
// @Param isResolved query bool false "Filter resolved or unresolved issues"
// @Success 200 {object} map[string]any
// @Failure 400 {object} map[string]any
// @Router /issues [get]
func (c *MainController) IssuesSearch(ctx *gin.Context) {
	var max int64
	issues := make([]models.IssueSearchResult, 0)

	container := ctx.Query("container")
	endTimestampQuery := ctx.Query("endTimestamp")
	issueSeverity := ctx.Query("issueSeverity")
	issueType := ctx.Query("issueType")
	limitQuery := ctx.Query("limit")
	offsetQuery := ctx.Query("offset")
	startTimestampQuery := ctx.Query("startTimestamp")
	_ = ctx.Query("searchString")

	isResolved, err := strconv.ParseBool(ctx.Query("isResolved"))
	if err != nil {
		isResolved = false
	}

	offset, err := strconv.Atoi(offsetQuery)
	if err != nil || offsetQuery == "" {
		offset = 0
	}

	limit, err := strconv.Atoi(limitQuery)
	if err != nil || limit > 100 || limitQuery == "" {
		limit = 30
	}

	startTimestamp, err := time.Parse(time.RFC3339, startTimestampQuery)
	if err != nil {
		fmt.Print("Error: ", err)
		startTimestamp = time.Time{}.UTC()
	}

	endTimestamp, err := time.Parse(time.RFC3339, endTimestampQuery)
	if err != nil || endTimestampQuery == "" {
		fmt.Print("Error: ", err)
		endTimestamp = time.Now().UTC()
	}

	qOpts := options.Find()
	qOpts.SetLimit(int64(limit))
	qOpts.SetSkip(int64(offset))
	qOpts.SetSort(bson.M{"timestamp": -1})
	qOpts.SetProjection(bson.M{
		"_id":           1,
		"containerName": 1,
		"severity":      1,
		"title":         1,
		"isResolved":    1,
		"timestamp":     1,
	})

	fmt.Print("startTimestamp: ", startTimestamp.UTC())
	fmt.Print("endTimestamp: ", endTimestamp.UTC())

	filter := bson.M{
		"isResolved": isResolved,
		"timestamp": bson.M{
			"$gte": startTimestamp.UTC(),
			"$lte": endTimestamp.UTC(),
		},
	}

	if container != "" {
		filter["containerName"] = container
	}

	if issueSeverity != "" {
		filter["severity"] = issueSeverity
	}

	if issueType != "" {
		filter["type"] = issueType
	}

	cursor, err := c.issuesCollection.Find(ctx, filter, qOpts)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	defer cursor.Close(ctx)

	for cursor.Next(ctx) {
		var issue models.IssueSearchResult

		if err := cursor.Decode(&issue); err != nil {
			continue
		}

		issues = append(issues, issue)
	}

	max, _ = c.issuesCollection.CountDocuments(ctx, filter)

	ctx.JSON(http.StatusOK, gin.H{
		"issues": issues,
		"max":    max,
	})
}

// GetIssue godoc
// @Summary Get information about a specific issue.
// @Description Get information about a specific issue by providing its ID.
// @Tags issues
// @Accept json
// @Produce json
// @Param id path string true "ID of the issue"
// @Success 200 {object} models.Issue
// @Failure 404 {object} map[string]any
// @Router /issues/{id} [get]
func (c *MainController) GetIssue(ctx *gin.Context) {
	var issue models.Issue
	id := ctx.Param("id")

	if err := c.issuesCollection.FindOne(ctx, bson.M{"_id": id}).Decode(&issue); err != nil {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
		return
	}

	ctx.JSON(http.StatusOK, issue)
}

func (c *MainController) RateIssue(ctx *gin.Context) {
	var issue models.Issue
	var issueRateReq models.IssueRateRequest
	var user models.User

	userId, err := getUserIdFromToken(ctx)
	if err != nil {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	//TODO: Remove hardcoded userId
	userId = "4c78e05c-2f83-4e6e-b4c1-8721618a1c89"

	err = ctx.ShouldBindJSON(&issueRateReq)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}

	if *issueRateReq.Score != -1 && *issueRateReq.Score != 0 && *issueRateReq.Score != 1 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Score must be one of: -1, 0, 1"})
		return
	}

	userResult := c.usersCollection.FindOne(ctx, bson.M{"userId": userId})

	err = userResult.Decode(&user)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	id := ctx.Param("id")

	issueConditions := bson.M{
		"_id":    id,
		"userId": userId,
	}

	filter := utils.GenerateFilter(issueConditions, "$and")
	issueResult := c.issuesCollection.FindOne(ctx, filter)

	err = issueResult.Decode(&issue)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var currentIssueScore = issue.Score

	if currentIssueScore == *issueRateReq.Score {
		ctx.JSON(http.StatusOK, gin.H{"message": "Issue already rated with the same score"})
		return
	}

	updatedIssueResult, err := c.issuesCollection.UpdateOne(ctx,
		filter,
		bson.M{
			"$set": bson.M{
				"score": issueRateReq.Score,
			},
		})
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if updatedIssueResult.MatchedCount == 0 {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "Issue cannot be found"})
		return
	}

	counter := user.Counter
	counter = utils.CalculateNewCounter(currentIssueScore, *issueRateReq.Score, counter)

	updatedUserResult, err := c.usersCollection.UpdateOne(ctx,
		bson.M{"userId": userId},
		bson.M{
			"$set": bson.M{
				"counter": counter,
			},
		})
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if updatedUserResult.MatchedCount == 0 {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "User cannot be found"})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"message": "Success",
	})
}

// ResolveIssue godoc
// @Summary Resolve an issue by setting its status to resolved.
// @Description Resolve an issue by providing its ID and updating its status to resolved.
// @Tags issues
// @Accept json
// @Produce json
// @Param id path string true "ID of the issue to be resolved"
// @Success 200 {object} map[string]any
// @Failure 404 {object} map[string]any
// @Failure 500 {object} map[string]any
// @Router /issues/resolve/{id} [post]
// @RequestBody application/json ResolveIssueRequest true "Issue resolution request"
func (c *MainController) ResolveIssue(ctx *gin.Context) {
	id := ctx.Param("id")

	res, err := c.issuesCollection.UpdateOne(ctx,
		bson.M{"_id": id},
		bson.M{
			"$set": bson.M{
				"isResolved": true,
			},
		})

	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if res.MatchedCount == 0 {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"message": "Success",
	})
}

// DeleteIssues godoc
// @Summary Delete issues based on the provided container name.
// @Description Delete issues based on the provided container name.
// @Tags issues
// @Accept json
// @Produce json
// @Param container query string true "Container name to delete issues from"
// @Success 200 {object} map[string]any
// @Failure 500 {object} map[string]any
// @Router /issues [delete]
func (c *MainController) DeleteIssues(ctx *gin.Context) {
	container := ctx.Query("container")
	fmt.Print("Container: ", container)
	res, err := c.issuesCollection.DeleteMany(ctx, bson.M{"containerName": container})
	if err != nil {
		ctx.JSON(500, gin.H{"error": err.Error()})
		return
	}
	ctx.JSON(200, gin.H{
		"message": "Success",
		"count":   res.DeletedCount,
	})
}

// GetContainers godoc
// @Summary Get a list of containers based on the provided user ID.
// @Description Get a list of containers based on the provided user ID.
// @Tags containers
// @Accept json
// @Produce json
// @Param userId query string true "User ID to filter containers"
// @Success 200 {array} string
// @Failure 500 {object} map[string]any
// @Router /containers [get]
func (c *MainController) GetContainers(ctx *gin.Context) {
	containers := make([]string, 0)

	userId, err := getUserIdFromToken(ctx)
	if err != nil {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	results, err := c.issuesCollection.Distinct(ctx, "containerName", bson.M{"userId": userId})
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, result := range results {
		if container, ok := result.(string); ok {
			containers = append(containers, container)
		}
	}
	ctx.JSON(http.StatusOK, containers)
}

// Auth Handlers
func (c *MainController) LoginWithGithubHandler(ctx *gin.Context) {
	var requestData models.GithubTokenRequest
	var user models.User

	if err := ctx.ShouldBindJSON(&requestData); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var userData, err = getGithubData(requestData.Code)
	if err != nil {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	userResult := c.usersCollection.FindOne(ctx, bson.M{"userId": strconv.Itoa(userData.Id)})

	err = userResult.Decode(&user)

	if err != nil && err.Error() != mongo.ErrNoDocuments.Error() {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err != nil && err.Error() == mongo.ErrNoDocuments.Error() {
		user = models.User{
			UserId:           strconv.Itoa(userData.Id),
			UserName:         userData.Login,
			IsPro:            false,
			AgentBearerToken: "",
			Counter:          0,
			Type:             "github",
		}

		_, err = c.usersCollection.InsertOne(ctx, user)
		if err != nil {
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	accessTokenString, err := createToken(user.UserId, user.UserName, false)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "couldn't make authentication token"})
		return
	}

	refreshTokenString, err := createToken(user.UserId, user.UserName, true)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "couldn't make authentication token"})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"message":      "Success",
		"accessToken":  accessTokenString,
		"expiresIn":    int64(ACCESS_TOKEN_EXPIRATION_TIME) / int64(time.Second),
		"refreshToken": refreshTokenString,
	})
}

func (c *MainController) LoginWithGoogleHandler(ctx *gin.Context) {
	var requestData models.GoogleTokenRequest
	var user models.User

	if err := ctx.ShouldBindJSON(&requestData); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	claims, err := validateGoogleJWT(requestData.IdToken)
	if err != nil {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	userResult := c.usersCollection.FindOne(ctx, bson.M{"userId": claims.Subject})

	err = userResult.Decode(&user)

	if err != nil && err.Error() != mongo.ErrNoDocuments.Error() {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err != nil && err.Error() == mongo.ErrNoDocuments.Error() {
		user = models.User{
			UserId:           claims.Subject,
			UserName:         claims.FirstName,
			IsPro:            false,
			AgentBearerToken: "",
			Counter:          0,
			Type:             "google",
		}

		_, err = c.usersCollection.InsertOne(ctx, user)
		if err != nil {
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	accessTokenString, err := createToken(user.UserId, user.UserName, false)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "couldn't make authentication token"})
		return
	}

	refreshTokenString, err := createToken(user.UserId, user.UserName, true)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "couldn't make authentication token"})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"message":      "Success",
		"accessToken":  accessTokenString,
		"expiresIn":    int64(ACCESS_TOKEN_EXPIRATION_TIME) / int64(time.Second),
		"refreshToken": refreshTokenString,
	})
}

func (c *MainController) RefreshTokenHandler(ctx *gin.Context) {
	var cfg = config.GetInstance()
	var claims = &models.JWTClaimsWithUserData{}
	var data models.RefreshTokenRequest
	var SECRET_KEY = []byte(cfg.SignalOneSecret)

	if err := ctx.ShouldBindJSON(&data); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	token, err := jwt.ParseWithClaims(data.RefreshToken, claims, func(token *jwt.Token) (interface{}, error) {
		return SECRET_KEY, nil
	})

	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if !token.Valid {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
		return
	}

	accessTokenString, err := createToken(claims.Id, claims.UserName, false)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "couldn't make authentication token"})
		return
	}

	refreshTokenString, err := createToken(claims.Id, claims.UserName, true)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "couldn't make authentication token"})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"message":      "Success",
		"accessToken":  accessTokenString,
		"expiresIn":    int64(ACCESS_TOKEN_EXPIRATION_TIME) / int64(time.Second),
		"refreshToken": refreshTokenString,
	})
}

func getUserIdFromToken(ctx *gin.Context) (string, error) {
	bearerToken := ctx.GetHeader("Authorization")

	jwtToken := strings.TrimPrefix(bearerToken, "Bearer ")

	userId, err := VerifyToken(jwtToken)
	if err != nil {
		return "", err
	}
	return userId, nil
}

func createToken(id string, userName string, isRefreshToken bool) (string, error) {
	var cfg = config.GetInstance()
	var expTime time.Duration
	var SECRET_KEY = []byte(cfg.SignalOneSecret)

	if isRefreshToken {
		expTime = REFRESH_TOKEN_EXPIRATION_TIME
	} else {
		expTime = ACCESS_TOKEN_EXPIRATION_TIME
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256,
		jwt.MapClaims{
			"exp":      time.Now().Add(expTime).Unix(),
			"id":       id,
			"userName": userName,
		})

	tokenString, err := token.SignedString(SECRET_KEY)
	if err != nil {
		return "", err
	}

	return tokenString, nil
}

func getGithubData(code string) (models.GithubUserData, error) {
	var cfg = config.GetInstance()
	var githubData = models.GithubUserData{}
	var githubJWTData = models.GithubTokenResponse{}
	var httpClient = &http.Client{}

	ghJWTReqBody := map[string]string{
		"client_id":     cfg.GithubClientId,
		"client_secret": cfg.GithubClientSecret,
		"code":          code,
	}

	jsonData, _ := json.Marshal(ghJWTReqBody)

	ghJWTReq, err := http.NewRequest("POST", "https://github.com/login/oauth/access_token", bytes.NewBuffer(jsonData))
	if err != nil {
		return models.GithubUserData{}, err
	}

	ghJWTReq.Header.Set("Accept", "application/json")
	ghJWTReq.Header.Set("Content-Type", "application/json")

	ghJWTResp, err := httpClient.Do(ghJWTReq)
	if err != nil {
		return models.GithubUserData{}, err
	}

	ghJWTRespBody, err := io.ReadAll(ghJWTResp.Body)
	if err != nil {
		return models.GithubUserData{}, err
	}

	err = json.Unmarshal(ghJWTRespBody, &githubJWTData)
	if err != nil {
		return models.GithubUserData{}, err
	}

	ghUserDataReq, err := http.NewRequest("GET", "https://api.github.com/user", nil)
	if err != nil {
		return models.GithubUserData{}, err
	}

	ghUserDataReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", githubJWTData.AccessToken))

	ghUserDataResp, err := httpClient.Do(ghUserDataReq)
	if err != nil {
		return models.GithubUserData{}, err
	}

	ghUserDataRespBody, err := io.ReadAll(ghUserDataResp.Body)
	if err != nil {
		return models.GithubUserData{}, err
	}

	err = json.Unmarshal(ghUserDataRespBody, &githubData)
	if err != nil {
		return models.GithubUserData{}, err
	}

	return githubData, nil
}

func getGooglePublicKey(keyId string) (string, error) {
	var googleData = map[string]string{}

	resp, err := http.Get("https://www.googleapis.com/oauth2/v1/certs")
	if err != nil {
		return "", err
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	err = json.Unmarshal(data, &googleData)
	if err != nil {
		return "", err
	}

	key, ok := googleData[keyId]
	if !ok {
		return "", errors.New("key not found")
	}

	return key, nil
}

func validateGoogleJWT(tokenString string) (models.GoogleClaims, error) {
	var cfg = config.GetInstance()
	var claimsStruct = models.GoogleClaims{}

	token, err := jwt.ParseWithClaims(
		tokenString,
		&claimsStruct,
		func(token *jwt.Token) (interface{}, error) {
			pem, err := getGooglePublicKey(fmt.Sprintf("%s", token.Header["kid"]))
			if err != nil {
				return nil, err
			}

			key, err := jwt.ParseRSAPublicKeyFromPEM([]byte(pem))
			if err != nil {
				return nil, err
			}

			return key, nil
		},
	)

	if err != nil {
		return models.GoogleClaims{}, err
	}

	claims, ok := token.Claims.(*models.GoogleClaims)
	if !ok {
		return models.GoogleClaims{}, errors.New("invalid claims")
	}

	if claims.Issuer != "accounts.google.com" && claims.Issuer != "https://accounts.google.com" {
		return models.GoogleClaims{}, errors.New("iss is invalid")
	}

	audienceToCheck := cfg.GoogleClientId
	found := false

	for _, audience := range claims.Audience {
		if audience == audienceToCheck {
			found = true
			break
		}
	}

	if !found {
		return models.GoogleClaims{}, errors.New("aud is invalid")
	}

	if claims.ExpiresAt != nil && claims.ExpiresAt.Unix() < time.Now().UTC().Unix() {
		return models.GoogleClaims{}, errors.New("jwt is expired")
	}

	return *claims, nil
}

func VerifyToken(tokenString string) (string, error) {
	var cfg = config.GetInstance()
	var claims = &models.JWTClaimsWithUserData{}
	var SECRET_KEY = []byte(cfg.SignalOneSecret)

	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		return SECRET_KEY, nil
	})

	if err != nil {
		return "", err
	}

	if !token.Valid {
		return "", fmt.Errorf("invalid token")
	}

	return claims.Id, nil
}

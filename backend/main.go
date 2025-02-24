package main

import (
	"context"
	"net/http"
	"signalone/cmd/config"
	"signalone/pkg/controllers"
	"signalone/pkg/routers"

	_ "signalone/docs" // Import the generated docs package

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var InferenceHyperParameters = map[string]interface{}{
	"temperature":    0.7,
	"top_k":          20,
	"top_p":          0.9,
	"do_sample":      true,
	"max_new_tokens": 160,
}

var RAGHyperParameters = map[string]interface{}{
	"limit": 3,
}

// @title			SignalOne API
// @version		1.0
// @description	API for SignalOne application
// @host			localhost:8080
// @BasePath		/api
func main() {
	var (
		server = gin.Default()
	)
	cfg := config.GetInstance()
	if cfg == nil {
		panic("critical: unable to load config")
	}

	if cfg.Mode == "local" {
		server.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	}

	appDbClient, err := mongo.Connect(
		context.Background(),
		options.Client().ApplyURI(cfg.ApplicationDbUrl),
	)
	if err != nil {
		panic(err)
	}
	issuesCollectionClient := appDbClient.Database(cfg.ApplicationDbName).Collection(cfg.ApplicationIssuesCollectionName)
	usersCollectionClient := appDbClient.Database(cfg.ApplicationDbName).Collection(cfg.ApplicationUsersCollectionName)

	savedAnalysisDbClient, err := mongo.Connect(
		context.Background(),
		options.Client().ApplyURI(cfg.SavedAnalysisDbUrl),
	)
	if err != nil {
		panic(err)
	}
	savedAnalysisCollectionClient := savedAnalysisDbClient.Database(cfg.SavedAnalysisDbName).Collection(cfg.SavedAnalysisCollectionName)

	mainController := controllers.NewMainController(
		issuesCollectionClient,
		usersCollectionClient,
		savedAnalysisCollectionClient,
	)

	//authController TBD
	corsConfig := cors.DefaultConfig()
	corsConfig.AllowOrigins = []string{"*"}
	corsConfig.AllowHeaders = []string{"*"}
	corsConfig.AllowCredentials = true

	server.Use(cors.New(corsConfig))

	router := server.Group("/api")
	router.GET("/healthz", func(ctx *gin.Context) {
		message := "signal api is up and running, operational subsystems: {}"
		ctx.JSON(http.StatusOK, gin.H{"status": "success", "message": message})
	})

	routeController := routers.NewMainRouter(mainController)
	routeController.RegisterRoutes(router)

	server.Run(":" + cfg.ServerPort)
}

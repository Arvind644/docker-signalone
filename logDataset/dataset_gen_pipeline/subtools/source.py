import os
import json
import csv
import shutil
import requests
import html

from helpers import helpers
from subtools.subtool import Subtool
from docker import DockerClient

class SourceSubtool(Subtool):
    def execute(self, args):
        if args.action == 'scrape':
            self.__scrape(args)
        elif args.action == 'push':
            self.__push(args)
        elif args.action == 'generate':
            self.__generate(args)
        else:
            print("Action not supported")
            exit(1)


    def __scrape(self, args):
        if args.source == 'stackoverflow':
            self.__scrapeStackOverflow()
        elif args.source == 'github':
            self.__scrapeGithub()
        elif args.source == 'docker':
            self.__pullDockerLogs()
        elif args.source == 'elasticsearch':
            self.__pullElasticsearchLogs()

    def __push(self, args):
        if args.format == 'json':
            self.__pushJsonLogs(args.log_file)
        elif args.format == 'csv':
            self.__pushCsvLogs(args.log_file)
        elif args.format == 'plain':
            self.__pushPlainLogs(args.log_file)
    
    def __pull(self, args):
        if args.source == 'docker':
            self.__pullDockerLogs()
        elif args.source == 'elasticsearch':
            self.__pullElasticsearchLogs()
    
    def __generate(self, args):
        self.__generateSyntheticSourceLogs(args.base_logs_file)

    def __scrapeStackOverflow(self):
        print("Scraping StackOverflow")
        body_log_keywords = ["logs are showing", "logs I get"]
        body_logs_validators = ["stderr", "ERROR", "WARN", "WARNING",
                                 "Exception", "exception", "error", "Error", "warn",
                                   "Warn", "Warning", "signal" , "INFO", "info", "Info",
                                   "exit", "code", "Code", "CODE", "traceback", "Traceback"]
        body_logs_excluders = ["{", "}", "\t", "if", "def", "func", "class", "=", ";"]
        log_markdown_begin = ["\r\n```\r\n", "`"]
        log_markdown_end = ["\r\n```\r\n", "`"]
        query = "https://api.stackexchange.com/2.3/search/advanced?pagesize=25&page={}&order=desc&sort=creation&answers=1&site=stackoverflow&filter=!*236eb_eL9rai)MOSNZ-6D3Q6ZKb0buI*IVotWaTb&body={}"
        discovered_logs = []
        pages = 2
        page = 1
        for keyword in body_log_keywords:
            page = 1
            while page <= pages:
                response = requests.get(query.format(page, keyword))
                if response.status_code == 200:
                    response = json.loads(response.text)
                    items_num = len(response["items"])
                    for itemid, item in enumerate(response["items"]):
                        body = item["body_markdown"]
                        print('Processing {} out of {} items'.format(itemid+1, items_num))
                        for lidx, log_begin in enumerate(log_markdown_begin):
                            while log_begin in body:
                                body_idx_start_seq = body.find(log_begin) + len(log_begin)
                                body_idx_end_seq = body[body_idx_start_seq:].find(log_markdown_end[lidx])
                                log = body[body_idx_start_seq:body_idx_start_seq + body_idx_end_seq]
                                if (any(validator in log for validator in body_logs_validators) and 
                                    all(log.count(excluder) < 5 for excluder in body_logs_excluders)):
                                    discovered_logs.append(log)
                                    body = body.replace(log_begin, "", 1)
                                    body = body.replace(log_markdown_end[lidx], "", 1)
                                else:
                                    body = body.replace(log_begin, "", 1)
                                    body = body.replace(log_markdown_end[lidx], "", 1)
                response = None
                print('Processed {} page out of {}'.format(page, pages))
                page += 1
        self.__pushLogs(discovered_logs, batch_size_def=1)
                    
                        
    def __scrapeGithub(self):
        print("Scraping Github")
        raise NotImplementedError()
    
    def __generateSyntheticSourceLogs(self, baseLogsFile):
        print("Generating synthetic source logs")
        raise NotImplementedError()
    
    def __pushJsonLogs(self, logFile):
        print("Pushing JSON logs to sources")
        key="Body"
        logs = []
        with open(logFile, 'r') as f:
            rawlogs = json.load(f)
            f.close()
        for log in rawlogs:
            try:
                logs.append(log[key])
            except KeyError:
                print("Key {} not found in log".format(key))
                continue
        self.__pushLogs(logs)
           
    def __pushCsvLogs(self, logFile):
        print("Pushing CSV logs to sources")
        with open(logFile, 'r') as f:
            csv_reader = csv.reader(f)
            logs = [row for row in csv_reader]
            f.close()
        self.__pushLogs(logs)
    
    def __pushPlainLogs(self, logFile):
        print("Pushing plain logs to sources")
        with open(logFile, 'r') as f:
            logs = f.readlines()
            f.close()
        self.__pushLogs(logs)
    
    def __pushLogs(self, logs, batch_size_def=5):
        log_batch_string = ""
        batch_size = batch_size_def
        log_batch = []
        for log in logs:
            if batch_size <= 0:
                log_batch.append(log_batch_string)
                log_batch_string = ""
                batch_size = batch_size_def
            batch_size -= 1
            log_batch_string += log + "\n"
        if log_batch_string != "":
            log_batch.append(log_batch_string)
        file_template = 'collected_logs_v'
        current_version = self.__getNewSourceVersion(file_template)
        new_version = helpers.updateMinorVersion(current_version)
        current_file = file_template.replace('v', str(current_version))
        new_file  = file_template.replace('v', str(new_version))
        sources_path = '../sources/'
        shutil.copyfile(sources_path + current_file + '.csv', sources_path + new_file + '.csv')
        with open(sources_path + new_file + '.csv', 'a', encoding="utf-8") as f:
            csv_writer = csv.writer(f, delimiter='\n')
            for log in log_batch:
                log = self.__logCleanup(log)
                csv_writer.writerow([log])

    def __pullDockerLogs(self):
        print("Pulling Docker logs")
        dc = DockerClient()
        containers = dc.containers.list(all=True)
        for container in containers:
            raw_logs = container.logs()
            logs = raw_logs.decode('utf-8').split('\n')
            self.__pushLogs(logs)
    
    def __pullElasticsearchLogs(self):
        print("Pulling Elasticsearch logs")
        raise NotImplementedError()
    
    def __getNewSourceVersion(self, source_file_template):
        files = os.listdir('../sources')
        files_matching_template = [file for file in files if source_file_template in file]
        files_matching_template.sort()
        if len(files_matching_template) == 0:
            return "v0.0.1"
        else:
            return files_matching_template[-1].split('_')[-1].replace('.csv', '')
        
    def __logCleanup(self, log):
        log = log.replace(',', ' ')
        log = log.replace('\n', ' ')
        log = log.replace('\r', ' ')
        log = log.replace('\t', ' ')
        log = html.unescape(log)
        return log
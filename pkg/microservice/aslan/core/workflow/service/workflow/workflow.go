/*
Copyright 2021 The KodeRover Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/hashicorp/go-multierror"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/koderover/zadig/pkg/microservice/aslan/config"
	commonmodels "github.com/koderover/zadig/pkg/microservice/aslan/core/common/repository/models"
	commonrepo "github.com/koderover/zadig/pkg/microservice/aslan/core/common/repository/mongodb"
	"github.com/koderover/zadig/pkg/microservice/aslan/core/common/repository/mongodb/template"
	commonservice "github.com/koderover/zadig/pkg/microservice/aslan/core/common/service"
	"github.com/koderover/zadig/pkg/microservice/aslan/core/common/service/nsq"
	"github.com/koderover/zadig/pkg/microservice/aslan/core/common/service/webhook"
	"github.com/koderover/zadig/pkg/setting"
	"github.com/koderover/zadig/pkg/shared/codehost"
	"github.com/koderover/zadig/pkg/shared/poetry"
	e "github.com/koderover/zadig/pkg/tool/errors"
	"github.com/koderover/zadig/pkg/types"
	"github.com/koderover/zadig/pkg/types/permission"
)

var mut sync.Mutex

type EnvStatus struct {
	EnvName    string `json:"env_name,omitempty"`
	Status     string `json:"status"`
	ErrMessage string `json:"err_message"`
}

func AutoCreateWorkflow(productName string, log *zap.SugaredLogger) *EnvStatus {
	productTmpl, err := template.NewProductColl().Find(productName)
	if err != nil {
		errMsg := fmt.Sprintf("[ProductTmpl.Find] %s error: %v", productName, err)
		log.Error(errMsg)
		return &EnvStatus{Status: setting.ProductStatusFailed, ErrMessage: errMsg}
	}
	errList := new(multierror.Error)
	mut.Lock()
	defer func() {
		mut.Unlock()
	}()

	workflowNames := []string{productName + "-workflow-dev", productName + "-workflow-qa", productName + "-workflow-ops"}
	// 云主机场景不创建ops工作流
	if productTmpl.ProductFeature != nil && productTmpl.ProductFeature.BasicFacility == "cloud_host" {
		workflowNames = []string{productName + "-workflow-dev", productName + "-workflow-qa"}
	}
	workflowSlice := sets.NewString()
	for _, workflowName := range workflowNames {
		_, err := FindWorkflow(workflowName, log)
		if err == nil {
			workflowSlice.Insert(workflowName)
		}
	}
	if len(workflowSlice) < len(workflowNames) {
		preSetResps, err := PreSetWorkflow(productName, log)
		if err != nil {
			errList = multierror.Append(errList, err)
		}
		buildModules := make([]*commonmodels.BuildModule, 0)
		artifactModules := make([]*commonmodels.ArtifactModule, 0)
		for _, preSetResp := range preSetResps {
			buildModule := &commonmodels.BuildModule{
				Target:         preSetResp.Target,
				BuildModuleVer: setting.Version,
			}
			buildModules = append(buildModules, buildModule)

			artifactModule := &commonmodels.ArtifactModule{
				Target: preSetResp.Target,
			}
			artifactModules = append(artifactModules, artifactModule)
		}

		for _, workflowName := range workflowNames {
			if workflowSlice.Has(workflowName) {
				continue
			}
			if _, err := commonrepo.NewWorkflowColl().Find(workflowName); err == nil {
				errList = multierror.Append(errList, fmt.Errorf("workflow [%s] 在项目 [%s] 中已经存在", workflowName, productName))
			}
			workflow := new(commonmodels.Workflow)
			workflow.Enabled = true
			workflow.ProductTmplName = productName
			workflow.Name = workflowName
			workflow.CreateBy = setting.SystemUser
			workflow.UpdateBy = setting.SystemUser
			workflow.EnvName = "dev"
			workflow.BuildStage = &commonmodels.BuildStage{
				Enabled: true,
				Modules: buildModules,
			}

			if strings.Contains(workflowName, "qa") {
				workflow.EnvName = "qa"
			}

			if strings.Contains(workflowName, "ops") {
				//如果是开启artifactStage，则关闭buildStage
				workflow.BuildStage.Enabled = false
				workflow.ArtifactStage = &commonmodels.ArtifactStage{
					Enabled: true,
					Modules: artifactModules,
				}
				workflow.EnvName = "ops"
			}

			workflow.Schedules = &commonmodels.ScheduleCtrl{
				Enabled: false,
				Items:   []*commonmodels.Schedule{},
			}
			workflow.TestStage = &commonmodels.TestStage{
				Enabled:   false,
				TestNames: []string{},
			}
			workflow.NotifyCtl = &commonmodels.NotifyCtl{
				Enabled:       false,
				NotifyTypes:   []string{},
				WeChatWebHook: "",
			}
			workflow.HookCtl = &commonmodels.WorkflowHookCtrl{
				Enabled: false,
				Items:   []*commonmodels.WorkflowHook{},
			}
			workflow.DistributeStage = &commonmodels.DistributeStage{
				Enabled:     false,
				S3StorageID: "",
				ImageRepo:   "",
				JumpBoxHost: "",
				Releases:    []commonmodels.RepoImage{},
				Distributes: []*commonmodels.ProductDistribute{},
			}

			if err := commonrepo.NewWorkflowColl().Create(workflow); err != nil {
				errList = multierror.Append(errList, err)
			}
		}
		if err = errList.ErrorOrNil(); err != nil {
			return &EnvStatus{Status: setting.ProductStatusFailed, ErrMessage: err.Error()}
		}
		return &EnvStatus{Status: setting.ProductStatusCreating}
	} else if len(workflowSlice) == len(workflowNames) {
		return &EnvStatus{Status: setting.ProductStatusSuccess}
	}
	return nil
}

func FindWorkflow(workflowName string, log *zap.SugaredLogger) (*commonmodels.Workflow, error) {
	resp, err := commonrepo.NewWorkflowColl().Find(workflowName)
	if err != nil {
		log.Errorf("Workflow.Find error: %v", err)
		return resp, e.ErrFindWorkflow.AddDesc(err.Error())
	}
	if resp.Schedules == nil {
		schedules, err := commonrepo.NewCronjobColl().List(&commonrepo.ListCronjobParam{
			ParentName: resp.Name,
			ParentType: config.WorkflowCronjob,
		})
		if err != nil {
			log.Errorf("cannot list cron job list, the error is: %v", err)
			return nil, e.ErrFindWorkflow.AddDesc(err.Error())
		}

		scheduleList := []*commonmodels.Schedule{}
		for _, v := range schedules {
			scheduleList = append(scheduleList, &commonmodels.Schedule{
				ID:           v.ID,
				Number:       v.Number,
				Frequency:    v.Frequency,
				Time:         v.Time,
				MaxFailures:  v.MaxFailure,
				TaskArgs:     v.TaskArgs,
				WorkflowArgs: v.WorkflowArgs,
				TestArgs:     v.TestArgs,
				Type:         config.ScheduleType(v.JobType),
				Cron:         v.Cron,
				Enabled:      v.Enabled,
			})
		}
		schedule := commonmodels.ScheduleCtrl{
			Enabled: resp.ScheduleEnabled,
			Items:   scheduleList,
		}
		resp.Schedules = &schedule
	}
	return resp, nil
}

type PreSetResp struct {
	Target          *commonmodels.ServiceModuleTarget `json:"target"`
	BuildModuleVers []string                          `json:"build_module_vers"`
	Deploy          []DeployEnv                       `json:"deploy"`
	Repos           []*types.Repository               `json:"repos"`
}

type DeployEnv struct {
	Env         string `json:"env"`
	Type        string `json:"type"`
	ProductName string `json:"product_name,omitempty"`
}

func PreSetWorkflow(productName string, log *zap.SugaredLogger) ([]*PreSetResp, error) {
	resp := make([]*PreSetResp, 0)
	targets := make(map[string][]DeployEnv)
	productTmpl, err := template.NewProductColl().Find(productName)
	if err != nil {
		log.Errorf("[%s] ProductTmpl.Find error: %v", productName, err)
		return resp, e.ErrGetTemplate.AddDesc(err.Error())
	}
	maxServiceTmpls, err := commonrepo.NewServiceColl().ListMaxRevisions()
	if err != nil {
		log.Errorf("ServiceTmpl.ListMaxRevisions error: %v", err)
		return resp, e.ErrListTemplate.AddDesc(err.Error())
	}

	for _, services := range productTmpl.Services {
		for _, service := range services {
			for _, serviceTmpl := range findServicesByName(service, maxServiceTmpls) {
				switch serviceTmpl.Type {
				case setting.K8SDeployType:
					for _, container := range serviceTmpl.Containers {
						deployEnv := DeployEnv{Env: service + "/" + container.Name, Type: setting.K8SDeployType, ProductName: serviceTmpl.ProductName}
						target := fmt.Sprintf("%s%s%s%s%s", serviceTmpl.ProductName, SplitSymbol, serviceTmpl.ServiceName, SplitSymbol, container.Name)
						targets[target] = append(targets[target], deployEnv)
					}
				case setting.PMDeployType:
					deployEnv := DeployEnv{Env: service, Type: setting.PMDeployType, ProductName: productName}
					target := fmt.Sprintf("%s%s%s%s%s", serviceTmpl.ProductName, SplitSymbol, serviceTmpl.ServiceName, SplitSymbol, serviceTmpl.ServiceName)
					targets[target] = append(targets[target], deployEnv)
				case setting.HelmDeployType:
					for _, container := range serviceTmpl.Containers {
						deployEnv := DeployEnv{Env: service + "/" + container.Name, Type: setting.HelmDeployType, ProductName: serviceTmpl.ProductName}
						target := fmt.Sprintf("%s%s%s%s%s", serviceTmpl.ProductName, SplitSymbol, serviceTmpl.ServiceName, SplitSymbol, container.Name)
						targets[target] = append(targets[target], deployEnv)
					}
				}
			}
		}
	}

	moList, err := commonrepo.NewBuildColl().List(&commonrepo.BuildListOption{})
	if err != nil {
		log.Errorf("[Build.List] error: %v", err)
		return nil, e.ErrListBuildModule.AddErr(err)
	}
	for k, v := range targets {
		// 选择了一个特殊字符在项目名称、服务名称以及服务组件名称里面都不允许的特殊字符，避免出现异常
		targetArr := strings.Split(k, SplitSymbol)
		if len(targetArr) != 3 {
			continue
		}

		preSet := &PreSetResp{
			Target: &commonmodels.ServiceModuleTarget{
				ProductName:   targetArr[0],
				ServiceName:   targetArr[1],
				ServiceModule: targetArr[2],
			},
			Deploy:          v,
			BuildModuleVers: []string{},
			Repos:           make([]*types.Repository, 0),
		}

		for _, mo := range moList {
			for _, moTarget := range mo.Targets {
				moduleTargetStr := fmt.Sprintf("%s%s%s%s%s", moTarget.ProductName, SplitSymbol, moTarget.ServiceName, SplitSymbol, moTarget.ServiceModule)
				if moduleTargetStr == k {
					preSet.BuildModuleVers = append(preSet.BuildModuleVers, mo.Version)
					if len(mo.Repos) == 0 {
						preSet.Repos = make([]*types.Repository, 0)
					} else {
						preSet.Repos = mo.Repos
					}
				}
			}
		}
		resp = append(resp, preSet)
	}
	return resp, nil
}

func findServicesByName(serviceName string, services []*commonmodels.Service) []*commonmodels.Service {
	resp := make([]*commonmodels.Service, 0)
	for _, service := range services {
		if service.ServiceName == serviceName {
			resp = append(resp, service)
		}
	}
	return resp
}

func CreateWorkflow(workflow *commonmodels.Workflow, log *zap.SugaredLogger) error {
	_, err := commonrepo.NewWorkflowColl().Find(workflow.Name)
	if err == nil {
		errStr := fmt.Sprintf("workflow [%s] 在项目 [%s] 中已经存在!", workflow.Name, workflow.ProductTmplName)
		return e.ErrUpsertWorkflow.AddDesc(errStr)
	}

	if !checkWorkflowSubModule(workflow) {
		return e.ErrUpsertWorkflow.AddDesc("workflow中没有子模块，请设置子模块")
	}

	if err := validateHookNames(workflow); err != nil {
		return e.ErrUpsertWorkflow.AddDesc(err.Error())
	}

	err = processWebhook(workflow.HookCtl.Items, nil, webhook.WorkflowPrefix+workflow.Name, log)
	if err != nil {
		log.Errorf("Failed to process webhook, err: %s", err)
		return e.ErrUpsertWorkflow.AddDesc(err.Error())
	}

	err = HandleCronjob(workflow, log)
	if err != nil {
		return e.ErrUpsertWorkflow.AddDesc(err.Error())
	}

	if err := commonrepo.NewWorkflowColl().Create(workflow); err != nil {
		log.Errorf("Workflow.Create error: %v", err)
		return e.ErrUpsertWorkflow.AddDesc(err.Error())
	}

	go CreateGerritWebhook(workflow, log)

	return nil
}

func checkWorkflowSubModule(workflow *commonmodels.Workflow) bool {
	if workflow.Schedules != nil && workflow.Schedules.Enabled {
		return true
	}
	if workflow.Slack != nil && workflow.Slack.Enabled {
		return true
	}
	if workflow.BuildStage != nil && workflow.BuildStage.Enabled {
		return true
	}
	if workflow.ArtifactStage != nil && workflow.ArtifactStage.Enabled {
		return true
	}
	if workflow.TestStage != nil && workflow.TestStage.Enabled {
		return true
	}
	if workflow.SecurityStage != nil && workflow.SecurityStage.Enabled {
		return true
	}
	if workflow.DistributeStage != nil && workflow.DistributeStage.Enabled {
		return true
	}
	if workflow.NotifyCtl != nil && workflow.NotifyCtl.Enabled {
		return true
	}
	if workflow.HookCtl != nil && workflow.HookCtl.Enabled {
		return true
	}

	return false
}

func UpdateWorkflow(workflow *commonmodels.Workflow, log *zap.SugaredLogger) error {
	if !checkWorkflowSubModule(workflow) {
		return e.ErrUpsertWorkflow.AddDesc("workflow中没有子模块，请设置子模块")
	}

	currentWorkflow, err := commonrepo.NewWorkflowColl().Find(workflow.Name)
	if err != nil {
		log.Errorf("Can not find workflow %s, err: %s", workflow.Name, err)
		return e.ErrUpsertWorkflow.AddDesc(err.Error())
	}

	if err := validateHookNames(workflow); err != nil {
		return e.ErrUpsertWorkflow.AddDesc(err.Error())
	}

	err = processWebhook(workflow.HookCtl.Items, currentWorkflow.HookCtl.Items, webhook.WorkflowPrefix+workflow.Name, log)
	if err != nil {
		log.Errorf("Failed to process webhook, err: %s", err)
		return e.ErrUpsertWorkflow.AddDesc(err.Error())
	}

	err = HandleCronjob(workflow, log)
	if err != nil {
		return e.ErrUpsertWorkflow.AddDesc(err.Error())
	}

	if err := UpdateGerritWebhook(workflow, log); err != nil {
		log.Errorf("UpdateGerritWebhook error: %v", err)
	}

	if workflow.TestStage != nil {
		for _, test := range workflow.TestStage.Tests {
			if test.Envs == nil {
				test.Envs = make([]*commonmodels.KeyVal, 0)
			}
		}
	}

	if err = commonrepo.NewWorkflowColl().Replace(workflow); err != nil {
		log.Errorf("Workflow.Update error: %v", err)
		return e.ErrUpsertWorkflow.AddDesc(err.Error())
	}

	return nil
}

func toHookSet(hooks interface{}) HookSet {
	res := NewHookSet()
	switch hs := hooks.(type) {
	case []*commonmodels.WorkflowHook:
		for _, h := range hs {
			res.Insert(hookItem{
				hookUniqueID: hookUniqueID{
					name:   h.MainRepo.Name,
					owner:  h.MainRepo.RepoOwner,
					repo:   h.MainRepo.RepoName,
					source: h.MainRepo.Source,
				},
				codeHostID: h.MainRepo.CodehostID,
			})
		}
	case []commonmodels.GitHook:
		for _, h := range hs {
			res.Insert(hookItem{
				hookUniqueID: hookUniqueID{
					name:  h.Name,
					owner: h.Owner,
					repo:  h.Repo,
				},
				codeHostID: h.CodehostID,
			})
		}
	}

	return res
}

func processWebhook(updatedHooks, currentHooks interface{}, name string, logger *zap.SugaredLogger) error {
	currentSet := toHookSet(currentHooks)
	updatedSet := toHookSet(updatedHooks)
	hooksToRemove := currentSet.Difference(updatedSet)
	hooksToAdd := updatedSet.Difference(currentSet)

	if hooksToRemove.Len() > 0 {
		logger.Debugf("Going to remove webhooks %+v", hooksToRemove)
	}
	if hooksToAdd.Len() > 0 {
		logger.Debugf("Going to add webhooks %+v", hooksToAdd)
	}

	var errs *multierror.Error
	var wg sync.WaitGroup

	for _, h := range hooksToRemove {
		wg.Add(1)
		go func(wh hookItem) {
			defer wg.Done()
			ch, err := codehost.GetCodeHostInfoByID(wh.codeHostID)
			if err != nil {
				logger.Errorf("Failed to get codeHost by id %d, err: %s", wh.codeHostID, err)
				errs = multierror.Append(errs, err)
				return
			}

			switch ch.Type {
			case setting.SourceFromGithub, setting.SourceFromGitlab:
				err = webhook.NewClient().RemoveWebHook(wh.name, wh.owner, wh.repo, ch.Address, ch.AccessToken, name, ch.Type)
				if err != nil {
					logger.Errorf("Failed to remove webhook %+v, err: %s", wh, err)
					errs = multierror.Append(errs, err)
					return
				}
			}
		}(h)
	}

	for _, h := range hooksToAdd {
		wg.Add(1)
		go func(wh hookItem) {
			defer wg.Done()
			ch, err := codehost.GetCodeHostInfoByID(wh.codeHostID)
			if err != nil {
				logger.Errorf("Failed to get codeHost by id %d, err: %s", wh.codeHostID, err)
				errs = multierror.Append(errs, err)
				return
			}

			switch ch.Type {
			case setting.SourceFromGithub, setting.SourceFromGitlab:
				err = webhook.NewClient().AddWebHook(wh.name, wh.owner, wh.repo, ch.Address, ch.AccessToken, name, ch.Type)
				if err != nil {
					logger.Errorf("Failed to add webhook %+v, err: %s", wh, err)
					errs = multierror.Append(errs, err)
					return
				}
			}
		}(h)
	}

	wg.Wait()

	return errs.ErrorOrNil()
}

func validateHookNames(p *commonmodels.Workflow) error {
	if p.HookCtl == nil {
		return nil
	}
	names := sets.NewString()
	for _, hook := range p.HookCtl.Items {
		if names.Has(hook.MainRepo.Name) {
			return fmt.Errorf("duplicated webhook name found: %s", hook.MainRepo.Name)
		}
		names.Insert(hook.MainRepo.Name)
	}

	return nil
}

func ListWorkflows(queryType string, userID int, log *zap.SugaredLogger) ([]*commonmodels.Workflow, error) {
	workflows, err := commonrepo.NewWorkflowColl().List(&commonrepo.ListWorkflowOption{})
	if err != nil {
		log.Errorf("Workflow.List error: %v", err)
		return workflows, e.ErrListWorkflow.AddDesc(err.Error())
	}

	favorites, err := commonrepo.NewFavoriteColl().List(&commonrepo.FavoriteArgs{UserID: userID, Type: string(config.WorkflowType)})
	if err != nil {
		log.Errorf("list favorite error: %v", err)
		return workflows, e.ErrListFavorite
	}

	workflowStats, err := commonrepo.NewWorkflowStatColl().FindWorkflowStat(&commonrepo.WorkflowStatArgs{Type: string(config.WorkflowType)})
	if err != nil {
		log.Errorf("list workflow stat error: %v", err)
		return workflows, fmt.Errorf("列出工作流统计失败")
	}

	for _, workflow := range workflows {
		if queryType == "artifact" {
			if workflow.ArtifactStage == nil || !workflow.ArtifactStage.Enabled {
				continue
			}
		}
		latestTask, _ := commonrepo.NewTaskColl().FindLatestTask(&commonrepo.FindTaskOption{PipelineName: workflow.Name, Type: config.WorkflowType})
		if latestTask != nil {
			workflow.LastestTask = &commonmodels.TaskInfo{TaskID: latestTask.TaskID, PipelineName: latestTask.PipelineName, Status: latestTask.Status}
		}

		latestPassedTask, _ := commonrepo.NewTaskColl().FindLatestTask(&commonrepo.FindTaskOption{PipelineName: workflow.Name, Type: config.WorkflowType, Status: config.StatusPassed})
		if latestPassedTask != nil {
			workflow.LastSucessTask = &commonmodels.TaskInfo{TaskID: latestPassedTask.TaskID, PipelineName: latestPassedTask.PipelineName}
		}

		latestFailedTask, _ := commonrepo.NewTaskColl().FindLatestTask(&commonrepo.FindTaskOption{PipelineName: workflow.Name, Type: config.WorkflowType, Status: config.StatusFailed})
		if latestFailedTask != nil {
			workflow.LastFailureTask = &commonmodels.TaskInfo{TaskID: latestFailedTask.TaskID, PipelineName: latestFailedTask.PipelineName}
		}

		workflow.IsFavorite = IsFavoriteWorkflow(workflow, favorites)

		workflow.TotalDuration, workflow.TotalNum, workflow.TotalSuccess = findWorkflowStat(workflow, workflowStats)
	}

	return workflows, nil
}

func IsFavoriteWorkflow(workflow *commonmodels.Workflow, favorites []*commonmodels.Favorite) bool {
	for _, favorite := range favorites {
		if workflow.Name == favorite.Name && workflow.ProductTmplName == favorite.ProductName {
			return true
		}
	}
	return false
}

func findWorkflowStat(workflow *commonmodels.Workflow, workflowStats []*commonmodels.WorkflowStat) (int64, int, int) {
	for _, workflowStat := range workflowStats {
		if workflow.Name == workflowStat.Name {
			return workflowStat.TotalDuration, workflowStat.TotalSuccess + workflowStat.TotalFailure, workflowStat.TotalSuccess
		}
	}
	return 0, 0, 0
}

func ListAllWorkflows(testName string, userID int, superUser bool, log *zap.SugaredLogger) ([]*commonmodels.Workflow, error) {
	allWorkflows := make([]*commonmodels.Workflow, 0)
	workflows := make([]*commonmodels.Workflow, 0)
	var err error
	if superUser {
		allWorkflows, err = commonrepo.NewWorkflowColl().List(&commonrepo.ListWorkflowOption{})
		if err != nil {
			log.Errorf("Workflow.List error: %v", err)
			return allWorkflows, e.ErrListWorkflow.AddDesc(err.Error())
		}
	} else {
		poetryCtl := poetry.New(config.PoetryAPIServer(), config.PoetryAPIRootKey())
		productNameMap, err := poetryCtl.GetUserProject(userID, log)
		if err != nil {
			log.Errorf("ListAllWorkflows GetUserProject error: %v", err)
			return nil, fmt.Errorf("ListAllWorkflows GetUserProject error: %v", err)
		}
		productNames := make([]string, 0)
		for productName, roleIDs := range productNameMap {
			roleID := roleIDs[0]
			if roleID == setting.RoleOwnerID {
				productNames = append(productNames, productName)
			} else {
				uuids, err := poetryCtl.GetUserPermissionUUIDs(roleID, productName, log)
				if err != nil {
					log.Errorf("ListAllWorkflows GetUserPermissionUUIDs error: %v", err)
					return nil, fmt.Errorf("ListAllWorkflows GetUserPermissionUUIDs error: %v", err)
				}

				ids := sets.NewString(uuids...)
				if ids.Has(permission.WorkflowUpdateUUID) {
					productNames = append(productNames, productName)
				}
			}
		}
		for _, productName := range productNames {
			tempWorkflows, err := commonrepo.NewWorkflowColl().List(&commonrepo.ListWorkflowOption{ProductName: productName})
			if err != nil {
				log.Errorf("ListAllWorkflows Workflow.List error: %v", err)
				return nil, fmt.Errorf("ListAllWorkflows Workflow.List error: %v", err)
			}
			allWorkflows = append(allWorkflows, tempWorkflows...)
		}
	}

	for _, workflow := range allWorkflows {
		if workflow.TestStage != nil {
			testNames := sets.NewString(workflow.TestStage.TestNames...)
			if testNames.Has(testName) {
				continue
			}
			for _, testEntity := range workflow.TestStage.Tests {
				if testEntity.Name == testName {
					continue
				}
			}
		}
		workflows = append(workflows, workflow)
	}
	return workflows, nil
}

func DeleteWorkflow(workflowName, requestID string, isDeletingProductTmpl bool, log *zap.SugaredLogger) error {
	opt := new(commonrepo.ListQueueOption)
	taskQueue, err := commonrepo.NewQueueColl().List(opt)
	if err != nil {
		log.Errorf("List queued task error: %v", err)
		return e.ErrDeletePipeline.AddErr(err)
	}
	// 当task还在运行时，先取消任务
	for _, task := range taskQueue {
		if task.PipelineName == workflowName && task.Type == config.WorkflowType {
			if err = commonservice.CancelTaskV2("system", task.PipelineName, task.TaskID, config.WorkflowType, requestID, log); err != nil {
				log.Errorf("task still running, cancel pipeline %s task %d", task.PipelineName, task.TaskID)
			}
		}
	}

	// 在删除前，先将workflow查出来，用于删除gerrit webhook
	workflow, err := commonrepo.NewWorkflowColl().Find(workflowName)
	if err != nil {
		log.Errorf("Workflow.Find error: %v", err)
		return e.ErrDeleteWorkflow.AddDesc(err.Error())
	}

	if !isDeletingProductTmpl {
		prod, err := template.NewProductColl().Find(workflow.ProductTmplName)
		if err != nil {
			log.Errorf("ProductTmpl.Find error: %v", err)
			return e.ErrDeleteWorkflow.AddErr(err)
		}
		if prod.OnboardingStatus != 0 {
			return e.ErrDeleteWorkflow.AddDesc("该工作流所属的项目处于onboarding流程中，不能删除工作流")
		}
	}

	err = processWebhook(nil, workflow.HookCtl.Items, webhook.WorkflowPrefix+workflow.Name, log)
	if err != nil {
		log.Errorf("Failed to process webhook, err: %s", err)
		return e.ErrUpsertWorkflow.AddDesc(err.Error())
	}

	go DeleteGerritWebhook(workflow, log)

	//删除所属的所有定时任务

	err = DeleteCronjob(workflow.Name, config.WorkflowCronjob)
	if err != nil {
		// FIXME: HOW TO DO THIS
		log.Errorf("Failed to delete %s 's cronjob, the error is: %v", workflow.Name, err)
		//return e.ErrDeleteWorkflow.AddDesc(err.Error())
	}
	payload := commonservice.CronjobPayload{
		Name:    workflow.Name,
		JobType: config.WorkflowCronjob,
		Action:  setting.TypeDisableCronjob,
	}
	pl, _ := json.Marshal(payload)
	err = nsq.Publish(config.TopicCronjob, pl)
	if err != nil {
		log.Errorf("Failed to publish to nsq topic: %s, the error is: %v", config.TopicCronjob, err)
		return e.ErrUpsertCronjob.AddDesc(err.Error())
	}

	if err := commonrepo.NewWorkflowColl().Delete(workflowName); err != nil {
		log.Errorf("Workflow.Find error: %v", err)
		return e.ErrDeleteWorkflow.AddDesc(err.Error())
	}

	if err := commonrepo.NewTaskColl().DeleteByPipelineNameAndType(workflowName, config.WorkflowType); err != nil {
		log.Errorf("PipelineTaskV2.DeleteByPipelineName error: %v", err)
	}

	if deliveryVersions, err := commonrepo.NewDeliveryVersionColl().Find(&commonrepo.DeliveryVersionArgs{OrgID: 1, WorkflowName: workflowName}); err == nil {
		for _, deliveryVersion := range deliveryVersions {
			if err := commonrepo.NewDeliveryVersionColl().Delete(deliveryVersion.ID.Hex()); err != nil {
				log.Errorf("DeleteWorkflow.DeliveryVersion.Delete error: %v", err)
			}

			if err = commonrepo.NewDeliveryBuildColl().Delete(deliveryVersion.ID.Hex()); err != nil {
				log.Errorf("DeleteWorkflow.DeliveryBuild.Delete error: %v", err)
			}

			if err = commonrepo.NewDeliveryDeployColl().Delete(deliveryVersion.ID.Hex()); err != nil {
				log.Errorf("DeleteWorkflow.DeliveryDeploy.Delete error: %v", err)
			}

			if err = commonrepo.NewDeliveryTestColl().Delete(deliveryVersion.ID.Hex()); err != nil {
				log.Errorf("DeleteWorkflow.DeliveryTest.Delete error: %v", err)
			}

			if err = commonrepo.NewDeliveryDistributeColl().Delete(deliveryVersion.ID.Hex()); err != nil {
				log.Errorf("DeleteWorkflow.DeliveryDistribute.Delete error: %v", err)
			}
		}
	}

	err = commonrepo.NewWorkflowStatColl().Delete(workflowName, string(config.WorkflowType))
	if err != nil {
		log.Errorf("WorkflowStat.Delete failed, error: %v", err)
	}

	if err := commonrepo.NewCounterColl().Delete("WorkflowTask:" + workflowName); err != nil {
		log.Errorf("Counter.Delete error: %v", err)
	}
	return nil
}

func CopyWorkflow(oldWorkflowName, newWorkflowName, username string, log *zap.SugaredLogger) error {
	oldWorkflow, err := commonrepo.NewWorkflowColl().Find(oldWorkflowName)
	if err != nil {
		log.Error(err)
		return e.ErrGetPipeline.AddErr(err)
	}
	_, err = commonrepo.NewWorkflowColl().Find(newWorkflowName)
	if err == nil {
		log.Error("new workflow already exists")
		return e.ErrExistsPipeline
	}
	oldWorkflow.UpdateBy = username
	oldWorkflow.Name = newWorkflowName
	oldWorkflow.ID = primitive.NewObjectID()

	return commonrepo.NewWorkflowColl().Create(oldWorkflow)
}

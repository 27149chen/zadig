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

package service

import (
	"fmt"
	"time"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/koderover/zadig/pkg/microservice/aslan/config"
	commonmodels "github.com/koderover/zadig/pkg/microservice/aslan/core/common/repository/models"
	commonrepo "github.com/koderover/zadig/pkg/microservice/aslan/core/common/repository/mongodb"
	templaterepo "github.com/koderover/zadig/pkg/microservice/aslan/core/common/repository/mongodb/template"
	commonservice "github.com/koderover/zadig/pkg/microservice/aslan/core/common/service"
	"github.com/koderover/zadig/pkg/setting"
	e "github.com/koderover/zadig/pkg/tool/errors"
)

type ServiceTmplBuildObject struct {
	ServiceTmplObject *commonservice.ServiceTmplObject `json:"pm_service_tmpl"`
	Build             *commonmodels.Build              `json:"build"`
}

func CreatePMService(username string, args *ServiceTmplBuildObject, log *zap.SugaredLogger) error {
	if isAdd, serviceLimit := addService(); !isAdd {
		return e.ErrCreateTemplate.AddDesc(fmt.Sprintf("现有服务数量已超过允许的最大值[%d]，请联系管理员查看", serviceLimit))
	}
	if len(args.ServiceTmplObject.ServiceName) == 0 {
		return e.ErrInvalidParam.AddDesc("服务名称为空，请检查")
	}
	if !config.ServiceNameRegex.MatchString(args.ServiceTmplObject.ServiceName) {
		return e.ErrInvalidParam.AddDesc("服务名称格式错误，请检查")
	}

	opt := &commonrepo.ServiceFindOption{
		ServiceName:   args.ServiceTmplObject.ServiceName,
		ExcludeStatus: setting.ProductStatusDeleting,
	}
	serviceTmpl, notFoundErr := commonrepo.NewServiceColl().Find(opt)
	if notFoundErr == nil {
		if serviceTmpl.ProductName != args.ServiceTmplObject.ProductName {
			return e.ErrInvalidParam.AddDesc(fmt.Sprintf("项目 [%s] %s", serviceTmpl.ProductName, "有相同的服务名称存在,请检查!"))
		}
	}

	serviceTemplate := fmt.Sprintf(setting.ServiceTemplateCounterName, args.ServiceTmplObject.ServiceName, args.ServiceTmplObject.Type)
	rev, err := commonrepo.NewCounterColl().GetNextSeq(serviceTemplate)
	if err != nil {
		return fmt.Errorf("get next pm service revision error: %v", err)
	}
	args.ServiceTmplObject.Revision = rev

	if err := commonrepo.NewServiceColl().Delete(args.ServiceTmplObject.ServiceName, args.ServiceTmplObject.Type, "", setting.ProductStatusDeleting, args.ServiceTmplObject.Revision); err != nil {
		log.Errorf("pmService.delete %s error: %v", args.ServiceTmplObject.ServiceName, err)
	}

	serviceObj := &commonmodels.Service{
		ServiceName:  args.ServiceTmplObject.ServiceName,
		Type:         args.ServiceTmplObject.Type,
		ProductName:  args.ServiceTmplObject.ProductName,
		Revision:     args.ServiceTmplObject.Revision,
		Visibility:   args.ServiceTmplObject.Visibility,
		HealthChecks: args.ServiceTmplObject.HealthChecks,
		EnvConfigs:   args.ServiceTmplObject.EnvConfigs,
		CreateTime:   time.Now().Unix(),
		CreateBy:     username,
		BuildName:    args.Build.Name,
	}

	if err := commonrepo.NewServiceColl().Create(serviceObj); err != nil {
		log.Errorf("pmService.Create %s error: %v", args.ServiceTmplObject.ServiceName, err)
		return e.ErrCreateTemplate.AddDesc(err.Error())
	}

	//创建构建
	if err := commonservice.CreateBuild(username, args.Build, log); err != nil {
		log.Errorf("pmService.Create build %s error: %v", args.Build.Name, err)
		if err2 := commonrepo.NewServiceColl().Delete(args.ServiceTmplObject.ServiceName, args.ServiceTmplObject.Type, "", setting.ProductStatusDeleting, args.ServiceTmplObject.Revision); err2 != nil {
			log.Errorf("pmService.delete %s error: %v", args.ServiceTmplObject.ServiceName, err2)
		}
		return e.ErrCreateTemplate.AddDesc(err.Error())
	}

	if notFoundErr != nil {
		if productTempl, err := commonservice.GetProductTemplate(args.ServiceTmplObject.ProductName, log); err == nil {
			//获取项目里面的所有服务
			if len(productTempl.Services) > 0 && !sets.NewString(productTempl.Services[0]...).Has(args.ServiceTmplObject.ServiceName) {
				productTempl.Services[0] = append(productTempl.Services[0], args.ServiceTmplObject.ServiceName)
			} else {
				productTempl.Services = [][]string{{args.ServiceTmplObject.ServiceName}}
			}
			//更新项目模板
			err = templaterepo.NewProductColl().Update(args.ServiceTmplObject.ProductName, productTempl)
			if err != nil {
				log.Errorf("CreatePMService Update %s error: %v", args.ServiceTmplObject.ServiceName, err)
				return e.ErrCreateTemplate.AddDesc(err.Error())
			}
		}
	}
	return nil
}

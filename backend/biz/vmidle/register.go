package vmidle

import (
	"github.com/samber/do"

	"github.com/63747756/jingjiaagent/backend/biz/vmidle/usecase"
)

func ProvideVMIdle(i *do.Injector) {
	do.Provide(i, usecase.NewVMIdleRefresher)
}

func InvokeVMIdle(i *do.Injector) {
	do.MustInvoke[usecase.VMIdleRefresher](i)
}

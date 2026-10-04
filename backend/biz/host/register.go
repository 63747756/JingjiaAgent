package host

import (
	"github.com/chaitin/MonkeyCode/backend/pkg/runtimeinstall"
	"github.com/samber/do"

	v1 "github.com/chaitin/MonkeyCode/backend/biz/host/handler/v1"
	"github.com/chaitin/MonkeyCode/backend/biz/host/repo"
	"github.com/chaitin/MonkeyCode/backend/biz/host/usecase"
)

// ProvideHost 注册 host 模块的服务工厂
func ProvideHost(i *do.Injector) {
	do.Provide(i, repo.NewHostRepo)
	do.Provide(i, runtimeinstall.NewService)
	do.Provide(i, usecase.NewHostUsecase)
	do.Provide(i, v1.NewHostHandler)
	do.Provide(i, v1.NewInternalHostHandler)
}

// ProvidePublicHost registers the existing standalone public-host selection.
// Embedded deployments can continue to provide their own implementation.
func ProvidePublicHost(i *do.Injector) {
	do.Provide(i, repo.NewPublicHostRepo)
	do.Provide(i, usecase.NewPublicHostUsecase)
}

// InvokeHost 触发 host 模块的 handler 初始化
func InvokeHost(i *do.Injector) {
	do.MustInvoke[*v1.HostHandler](i)
	do.MustInvoke[*v1.InternalHostHandler](i)
}

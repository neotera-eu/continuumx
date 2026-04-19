package metamanager

import (
	"github.com/kubeedge/api/apis/componentconfig/edgecore/v1alpha2"
	"github.com/kubeedge/beehive/pkg/core"
	beehiveContext "github.com/kubeedge/beehive/pkg/core/context"

	"github.com/neotera-eu/continuumx/edge/pkg/common/modules"
	metamanagerconfig "github.com/neotera-eu/continuumx/edge/pkg/metamanager/config"
	"github.com/neotera-eu/continuumx/edge/pkg/metamanager/dao/dbclient"
	"github.com/neotera-eu/continuumx/edge/pkg/metamanager/metaserver"
	metaserverconfig "github.com/neotera-eu/continuumx/edge/pkg/metamanager/metaserver/config"
	"github.com/neotera-eu/continuumx/edge/pkg/metamanager/metaserver/kubernetes/storage/sqlite/imitator"
	"github.com/neotera-eu/continuumx/pkg/features"
)

type metaManager struct {
	enable      bool
	metaService *dbclient.MetaService
}

var _ core.Module = (*metaManager)(nil)

func newMetaManager(enable bool) *metaManager {
	return &metaManager{
		enable:      enable,
		metaService: dbclient.NewMetaService(),
	}
}

// Register register metamanager
func Register(metaManager *v1alpha2.MetaManager) {
	metamanagerconfig.InitConfigure(metaManager)
	meta := newMetaManager(metaManager.Enable)
	core.Register(meta)
}

func (*metaManager) Name() string {
	return modules.MetaManagerModuleName
}

func (*metaManager) Group() string {
	return modules.MetaGroup
}

func (m *metaManager) Enable() bool {
	return m.enable
}

func (m *metaManager) RestartPolicy() *core.ModuleRestartPolicy {
	if !features.DefaultFeatureGate.Enabled(features.ModuleRestart) {
		return nil
	}
	return &core.ModuleRestartPolicy{
		RestartType:            core.RestartTypeOnFailure,
		IntervalTimeGrowthRate: 2.0,
	}
}

func (m *metaManager) Start() {
	if metaserverconfig.Config.Enable {
		imitator.StorageInit()
		go metaserver.NewMetaServer().Start(beehiveContext.Done())
	}

	m.runMetaManager()
}

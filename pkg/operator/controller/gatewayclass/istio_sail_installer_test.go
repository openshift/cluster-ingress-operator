package gatewayclass

import (
	"context"
	"fmt"

	"github.com/istio-ecosystem/sail-operator/pkg/install"
)

// fakeSailInstaller implements the Istio methods for installation
// We care for now just about Apply, Uninstall and Status for the reconciliation tests
type fakeSailInstaller struct {
	notifyCh     chan struct{}
	internalOpts install.Options
	status       install.Status
}

// Test if implementation adheres the interface
var _ SailLibraryInstaller = &fakeSailInstaller{}

func (i *fakeSailInstaller) Start(ctx context.Context) <-chan struct{} {
	i.notifyCh = make(chan struct{})
	return i.notifyCh
}

func (i *fakeSailInstaller) Apply(opts install.Options) {
	// This is where we fake behavior
	switch opts.Version {
	case "ok-helm":
		i.status.Installed = true
		i.status.CRDState = install.CRDManagedByCIO
	case "ok-olm":
		i.status.Installed = true
		i.status.CRDState = install.CRDManagedByOLM
	case "ok-mixed":
		i.status.Installed = true
		i.status.CRDState = install.CRDMixedOwnership
	case "broken":
		i.status.Error = fmt.Errorf("broken installation")
		i.status.CRDState = install.CRDUnknownManagement
	default:
		i.status.Installed = true
		i.status.CRDState = install.CRDNoneExist
	}
}

func (i *fakeSailInstaller) Uninstall(ctx context.Context, namespace, revision string) error {
	return nil
}

func (i *fakeSailInstaller) Status() install.Status {
	return i.status
}

func (i *fakeSailInstaller) Enqueue() {}

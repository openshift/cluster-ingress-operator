package util

import (
	"context"
	"testing"

	"github.com/go-logr/logr"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
)

type FakeCache struct {
	cache.Informers
	client.Reader
}

type FakeClientRecorder struct {
	client.Client
	*testing.T

	Added   []client.Object
	Updated []client.Object
	Deleted []client.Object

	StatusWriter *FakeStatusWriter
}

func (c *FakeClientRecorder) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *FakeClientRecorder) List(ctx context.Context, obj client.ObjectList, opts ...client.ListOption) error {
	return c.Client.List(ctx, obj, opts...)
}

func (c *FakeClientRecorder) Scheme() *runtime.Scheme {
	return c.Client.Scheme()
}

func (c *FakeClientRecorder) RESTMapper() meta.RESTMapper {
	return c.Client.RESTMapper()
}

func (c *FakeClientRecorder) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	c.Added = append(c.Added, obj)
	return c.Client.Create(ctx, obj, opts...)
}

func (c *FakeClientRecorder) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	c.Deleted = append(c.Deleted, obj)
	return c.Client.Delete(ctx, obj, opts...)
}

func (c *FakeClientRecorder) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	return c.Client.DeleteAllOf(ctx, obj, opts...)
}

func (c *FakeClientRecorder) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.Updated = append(c.Updated, obj)
	return c.Client.Update(ctx, obj, opts...)
}

func (c *FakeClientRecorder) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func (c *FakeClientRecorder) Status() client.StatusWriter {
	if c.StatusWriter == nil {
		return c.Client.Status()
	}
	return c.StatusWriter
}

type FakeStatusWriter struct {
	client.StatusWriter

	Added   []client.Object
	Updated []client.Object
}

func (w *FakeStatusWriter) Create(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	w.Added = append(w.Added, obj)
	return w.StatusWriter.Create(ctx, obj, subResource, opts...)
}

func (w *FakeStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	w.Updated = append(w.Updated, obj)
	return w.StatusWriter.Update(ctx, obj, opts...)
}

func (w *FakeStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return w.StatusWriter.Patch(ctx, obj, patch, opts...)
}

type FakeController struct {
	*testing.T
	// started indicates whether Start() has been called.
	Started bool
	// startNotificationChan is an optional channel by which a test can
	// receive a notification when Start() is called.
	StartNotificationChan chan struct{}
}

func (_ *FakeController) Reconcile(context.Context, reconcile.Request) (reconcile.Result, error) {
	return reconcile.Result{}, nil
}

func (_ *FakeController) Watch(_ source.Source) error {
	return nil
}

func (c *FakeController) Start(_ context.Context) error {
	if c.Started {
		c.T.Fatal("controller was started twice!")
	}
	c.Started = true
	if c.StartNotificationChan != nil {
		c.StartNotificationChan <- struct{}{}
	}
	return nil
}

func (_ *FakeController) GetLogger() logr.Logger {
	return logf.Logger
}

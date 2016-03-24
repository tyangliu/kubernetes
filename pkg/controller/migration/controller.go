package migration

import (
	"time"

	"github.com/golang/glog"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/apis/extensions"
	"k8s.io/kubernetes/pkg/client/cache"
	clientset "k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset"
	"k8s.io/kubernetes/pkg/client/record"
	unversionedcore "k8s.io/kubernetes/pkg/client/typed/generated/core/unversioned"
	"k8s.io/kubernetes/pkg/controller"
	"k8s.io/kubernetes/pkg/controller/framework"
	replicationcontroller "k8s.io/kubernetes/pkg/controller/replication"
	"k8s.io/kubernetes/pkg/runtime"
	utilruntime "k8s.io/kubernetes/pkg/util/runtime"
	"k8s.io/kubernetes/pkg/util/wait"
	"k8s.io/kubernetes/pkg/util/workqueue"
	"k8s.io/kubernetes/pkg/watch"
)

const (
	StoreSyncedPollPeriod = 100 * time.Millisecond
)

type MigrationController struct {
	kubeClient clientset.Interface
	podControl controller.PodControlInterface

	updateHandler func(m *extensions.Migration) error
	syncHandler   func(mKey string) error

	podStoreSynced func() bool

	migrationStore cache.StoreToMigrationLister
	migrationController *framework.Controller

	podStore cache.StoreToPodLister
	podController *framework.Controller

	queue *workqueue.Type
	recorder record.EventRecorder
}

func NewMigrationController(kubeClient clientset.Interface, resyncPeriod controller.ResyncPeriodFunc) *MigrationController {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&unversionedcore.EventSinkImpl{kubeClient.Core().Events("")})

	mc := &MigrationController {
		kubeClient: kubeClient,
		podControl: controller.RealPodControl{
			KubeClient: kubeClient,
			Recorder: eventBroadcaster.NewRecorder(api.EventSource{Component: "migration-controller"}),
		},
		queue: workqueue.New(),
		recorder: eventBroadcaster.NewRecorder(api.EventSource{Component: "migration-controller"}),
	}

	mc.migrationStore.Store, mc.migrationController = framework.NewInformer(
		&cache.ListWatch{
			ListFunc: func(options api.ListOptions) (runtime.Object, error) {
				return mc.kubeClient.Extensions().Migrations(api.NamespaceAll).List(options)
			},
			WatchFunc: func(options api.ListOptions) (watch.Interface, error) {
				return mc.kubeClient.Extensions().Migrations(api.NamespaceAll).Watch(options)
			},
		},
		&extensions.Migration{},
		replicationcontroller.FullControllerResyncPeriod,
		framework.ResourceEventHandlerFuncs{
			AddFunc:    mc.addMigrationNotification,
			UpdateFunc: mc.updateMigrationNotification,
			DeleteFunc: mc.deleteMigrationNotification,
		},
	)

	mc.podStore.Store, mc.podController = framework.NewInformer(
		&cache.ListWatch{
			ListFunc: func(options api.ListOptions) (runtime.Object, error) {
				return mc.kubeClient.Core().Pods(api.NamespaceAll).List(options)
			},
			WatchFunc: func(options api.ListOptions) (watch.Interface, error) {
				return mc.kubeClient.Core().Pods(api.NamespaceAll).Watch(options)
			},
		},
		&api.Pod{},
		resyncPeriod(),
		framework.ResourceEventHandlerFuncs{},
	)

	mc.updateHandler = mc.updateMigrationStatus
	mc.syncHandler = mc.syncMigration
	mc.podStoreSynced = mc.podController.HasSynced
	return mc
}

func (mc *MigrationController) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	go mc.migrationController.Run(stopCh)
	go mc.podController.Run(stopCh)
	for i := 0; i < workers; i++ {
		go wait.Until(mc.worker, time.Second, stopCh)
	}
	<-stopCh
	glog.Infof("Shutting down Migration Manager")
	mc.queue.ShutDown()
}

func (mc *MigrationController) addMigrationNotification(obj interface{}) {
	m := obj.(*extensions.Migration)
	glog.V(4).Infof("Adding migration %s", m.Name)
	mc.enqueueMigration(m)
}

func (mc *MigrationController) updateMigrationNotification(old, cur interface{}) {
	oldM := old.(*extensions.Migration)
	glog.V(4).Infof("Updating migration %s", oldM.Name)
	mc.enqueueMigration(cur.(*extensions.Migration))
}

func (mc *MigrationController) deleteMigrationNotification(obj interface{}) {
	m, ok := obj.(*extensions.Migration)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			glog.Errorf("Couldn't get object from tombstone %+v", obj)
			return
		}
		m, ok = tombstone.Obj.(*extensions.Migration)
		if !ok {
			glog.Errorf("Tombstone contained object that is not a Migration %+v", obj)
			return
		}
	}
	glog.V(4).Infof("Deleting migration %s", m.Name)
	mc.enqueueMigration(m)
}

func (mc *MigrationController) enqueueMigration(migration *extensions.Migration) {
	key, err := controller.KeyFunc(migration)
	if err != nil {
		glog.Errorf("Couldn't get key for object %+v: %v", migration, err)
		return
	}
	mc.queue.Add(key)
}

// worker runs a worker thread that just dequeues items, processes them, and marks them done.
// It enforces that the syncHandler is never invoked concurrently with the same key.
func (mc *MigrationController) worker() {
	for {
		func() {
			key, quit := mc.queue.Get()
			if quit {
				return
			}
			defer mc.queue.Done(key)
			err := mc.syncHandler(key.(string))
			if err != nil {
				glog.Errorf("Error syncing migration: %v", err)
			}
		}()
	}
}

func (mc *MigrationController) syncMigration(key string) error {
	startTime := time.Now()
	defer func() {
		glog.V(4).Infof("Finished syncing migration %q (%v)", key, time.Now().Sub(startTime))
	}()

	if !mc.podStoreSynced() {
		time.Sleep(StoreSyncedPollPeriod)
		glog.Info("Waiting for pod controller to sync, requeuing migration %s", key)
		mc.queue.Add(key)
		return nil
	}

	obj, exists, err := mc.migrationStore.Store.GetByKey(key)
	if err != nil {
		glog.Infof("Unable to retrieve migration %v from store: %v", key, err)
		mc.queue.Add(key)
		return err
	}
	if !exists {
		glog.Infof("Migration has been deleted %v", key)
		return nil
	}

	m := obj.(*extensions.Migration)

	// Update the migration status to Started
	m.Status.Phase = extensions.MigrationStarted
	if err := mc.updateHandler(m); err != nil {
		return err
	}

	// Attempt to get the pod with the pod name specified in the migration spec.

	return nil
}

func (mc *MigrationController) updateMigrationStatus(m *extensions.Migration) error {
	_, err := mc.kubeClient.Extensions().Migrations(m.Namespace).UpdateStatus(m)
	return err
}

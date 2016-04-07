package migration

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
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
	"k8s.io/kubernetes/pkg/labels"
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

	updateHandler func(m *extensions.Migration) (*extensions.Migration, error)
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
	oldM, newM := old.(*extensions.Migration), cur.(*extensions.Migration)

	// If a migration has not changed or has no change except within its status,
	// do not re-enqueue it.
	mungedM := *newM
	mungedM.Status = oldM.Status
	mungedM.ObjectMeta = oldM.ObjectMeta

	if api.Semantic.DeepEqual(mungedM, *oldM) {
		glog.V(4).Infof("Ignoring unchanged migration update %s", oldM.Name)
		return
	}

	glog.V(4).Infof("Updating migration %s", oldM.Name)
	mc.enqueueMigration(newM)
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

func (mc *MigrationController) getMigration(key string) (*extensions.Migration, error) {
	obj, exists, err := mc.migrationStore.Store.GetByKey(key)
	if err != nil {
		glog.Infof("Unable to retrieve migration %v from store: %v", key, err)
		mc.queue.Add(key)
		return nil, err
	}
	if !exists {
		glog.Infof("Migration has been deleted %v", key)
		return nil, nil
	}

	return obj.(*extensions.Migration), nil
}

// Clones a pod onto the destination node of the migration
// TODO: handle nil pod argument
func (mc *MigrationController) clonePod(m *extensions.Migration, pod *api.Pod) (map[string]string, error) {
	// Copy the pod spec, set DeferRun flag, and create the pod copy on the destination node
	podSpecClone, err := api.Scheme.DeepCopy(pod.Spec)
	if err != nil {
		glog.Errorf("Error copying source pod %s for migration: %v", m.Spec.PodName, err)
		return nil, err
	}

	newPodSpec := podSpecClone.(api.PodSpec)
	newPodSpec.DeferRun = true
	newPodSpec.NodeName = ""

	// Clone labels and add one for migration controller discoverability
	// TODO: make the label key universally unique, so it's near impossible to
	// clash with existing labels
	newLabels := make(map[string]string)
	for k,v := range pod.Labels {
  	newLabels[k] = v
	}
	newLabels["migration-controller"] = m.Name

	// Create clone pod on destination node
	template := &api.PodTemplateSpec{
		ObjectMeta: api.ObjectMeta{
			Labels: newLabels,
		},
		Spec: newPodSpec,
	}
	if err := mc.podControl.CreatePodsOnNode(m.Spec.DestNodeName, m.Namespace, template, m); err != nil {
		glog.Errorf("Error creating clone pod on destination node: %v", err)
		return nil, err
	}
	return newLabels, nil
}

// Clones a pod onto the destination node of the migration, and waits until the
// cloned pod is retrievable to return the pod
func (mc *MigrationController) cloneAndGetPod(m *extensions.Migration, pod *api.Pod) (*api.Pod, error) {
	newLabels, err := mc.clonePod(m, pod)
	if err != nil {
		return nil, err
	}

	var clonedPod *api.Pod
	// Wait until the clone pod has been created before continuing
	for {
		time.Sleep(500 * time.Millisecond)
		podList, err := mc.podStore.Pods(m.Namespace).List(labels.Set(newLabels).AsSelector())
		if err != nil {
			return nil, err
		}

		if len(podList.Items) == 0 {
			continue
		} else if len(podList.Items) > 1 {
			glog.Errorf("More than 1 pod cloned for migration")
		}

		clonedPod = &podList.Items[0]
		break
	}

	return clonedPod, nil
}

type CheckpointLocation struct {
	Path string
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

	m, err := mc.getMigration(key)

	// Update the migration status to Started
	m.Status.Phase = extensions.MigrationStarted
	if m, err = mc.updateHandler(m); err != nil {
		return err
	}

	// Attempt to get the pod with the pod name specified in the migration spec.
	pod, err := mc.kubeClient.Core().Pods(m.Namespace).Get(m.Spec.PodName)
	if err != nil {
		glog.Errorf("Error getting source pod %s for migration: %v", m.Spec.PodName, err)
		return err
	}

	clonePod, err := mc.cloneAndGetPod(m, pod)
	if err != nil {
		glog.Errorf("Error cloning pod %s: %v", m.Spec.PodName, err)
		return err
	}
	glog.V(4).Infof("Cloned pod: %+v", *clonePod)

	// Set should checkpoint flag to true, this will induce the the kubelet to
	// perform a checkpoint on all containers of the pod.
	pod.Spec.ShouldCheckpoint = true
	pod, err = mc.kubeClient.Core().Pods(m.Namespace).Update(pod)
	if err != nil {
		return err
	}

	// Wait until pod phase has been set to Checkpointed by Kubelet, indicating
	// completion
	for {
		time.Sleep(500 * time.Millisecond)
		pod, err = mc.kubeClient.Core().Pods(m.Namespace).Get(m.Spec.PodName)
		if err != nil {
			glog.Errorf("Error getting pod %s after checkpointing: %v", m.Spec.PodName, err)
			return err
		}
		if pod.Status.Phase != api.PodCheckpointed {
			continue
		}
		break
	}

	// Find the checkpoint image download path for the source pod
	srcNode, err := mc.kubeClient.Core().Nodes().Get(pod.Spec.NodeName)
	if err != nil {
		glog.Errorf("Error getting node for pod %s: %v", m.Spec.PodName, err)
		return err
	}

	// TODO: get address from the srcNode below instead?
	srcNodeAddr := pod.Status.HostIP

	kubeletPort := srcNode.Status.DaemonEndpoints.KubeletEndpoint.Port
	if kubeletPort == 0 {
		glog.Errorf("Invalid kubelet daemon port for node %s", srcNode.Name)
	}

	getPath := fmt.Sprintf("https://%s:%d/checkpoint/%s/%s", srcNodeAddr, kubeletPort, m.Namespace, pod.Name)
	glog.V(4).Infof("Checkpoint download URL: %s", getPath)

	// Find the checkpoint POST path for the destination pod
	destNode, err := mc.kubeClient.Core().Nodes().Get(clonePod.Spec.NodeName)
	if err != nil {
		glog.Errorf("Error getting node for pod %s: %v", m.Spec.PodName, err)
		return err
	}

	destNodeAddr := clonePod.Status.HostIP

	destKubeletPort := destNode.Status.DaemonEndpoints.KubeletEndpoint.Port
	if destKubeletPort == 0 {
		glog.Errorf("Invalid kubelet daemon port for node %s", destNode.Name)
	}

	postPath := fmt.Sprintf("https://%s:%d/checkpoint/%s/%s", destNodeAddr, destKubeletPort, m.Namespace, clonePod.Name)

	json, err := json.Marshal(CheckpointLocation{getPath})
	if err != nil {
		return err
	}

	// Skip certificate check for http request
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}

	_, err = client.Post(postPath, "application/json", bytes.NewBuffer(json))
	if err != nil {
		return err
	}

	return nil
}

func (mc *MigrationController) updateMigrationStatus(m *extensions.Migration) (*extensions.Migration, error) {
	return mc.kubeClient.Extensions().Migrations(m.Namespace).UpdateStatus(m)
}

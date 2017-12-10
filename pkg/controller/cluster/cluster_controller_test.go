/*
Copyright 2017 The Kubernetes Authors.

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

package cluster

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/uuid"
	clientgofake "k8s.io/client-go/kubernetes/fake"
	clientgotesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"

	clusteroperator "github.com/openshift/cluster-operator/pkg/apis/clusteroperator/v1alpha1"
	clusteroperatorclientset "github.com/openshift/cluster-operator/pkg/client/clientset_generated/clientset/fake"
	informers "github.com/openshift/cluster-operator/pkg/client/informers_generated/externalversions"
	"github.com/openshift/cluster-operator/pkg/controller"
)

func newTestClusterController() (
	*ClusterController,
	cache.Store, // cluster store
	cache.Store, // node group store
	*clientgofake.Clientset,
	*clusteroperatorclientset.Clientset,
) {
	kubeClient := &clientgofake.Clientset{}
	clusterOperatorClient := &clusteroperatorclientset.Clientset{}
	informers := informers.NewSharedInformerFactory(clusterOperatorClient, 0)

	controller := NewClusterController(
		informers.Clusteroperator().V1alpha1().Clusters(),
		informers.Clusteroperator().V1alpha1().NodeGroups(),
		kubeClient,
		clusterOperatorClient,
	)

	controller.clustersSynced = alwaysReady
	controller.nodeGroupsSynced = alwaysReady

	return controller,
		informers.Clusteroperator().V1alpha1().Clusters().Informer().GetStore(),
		informers.Clusteroperator().V1alpha1().NodeGroups().Informer().GetStore(),
		kubeClient,
		clusterOperatorClient
}

func filterInformerActions(actions []clientgotesting.Action) []clientgotesting.Action {
	ret := []clientgotesting.Action{}
	for _, action := range actions {
		if len(action.GetNamespace()) == 0 &&
			(action.Matches("list", "nodegroups") ||
				action.Matches("list", "clusters") ||
				action.Matches("watch", "nodegroups") ||
				action.Matches("watch", "clusters")) {
			continue
		}
		ret = append(ret, action)
	}

	return ret
}

func skipListerFunc(verb string, url url.URL) bool {
	if verb != "GET" {
		return false
	}
	if strings.HasSuffix(url.Path, "/nodegroups") || strings.Contains(url.Path, "/clusters") {
		return true
	}
	return false
}

var alwaysReady = func() bool { return true }

func getKey(cluster *clusteroperator.Cluster, t *testing.T) string {
	if key, err := controller.KeyFunc(cluster); err != nil {
		t.Errorf("Unexpected error getting key for Cluster %v: %v", cluster.Name, err)
		return ""
	} else {
		return key
	}
}

func newCluster(computeNames ...string) *clusteroperator.Cluster {
	cluster := &clusteroperator.Cluster{
		//TypeMeta: metav1.TypeMeta{APIVersion: api.Registry.GroupOrDie(v1.GroupName).GroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{
			UID:       uuid.NewUUID(),
			Name:      "cluster1",
			Namespace: metav1.NamespaceDefault,
			//ResourceVersion: "18",
		},
		Spec: clusteroperator.ClusterSpec{
			MasterNodeGroup: clusteroperator.ClusterNodeGroup{
				Size: 1,
			},
			ComputeNodeGroups: make([]clusteroperator.ClusterComputeNodeGroup, len(computeNames)),
		},
	}
	for i, name := range computeNames {
		cluster.Spec.ComputeNodeGroups[i] = clusteroperator.ClusterComputeNodeGroup{
			ClusterNodeGroup: clusteroperator.ClusterNodeGroup{
				Size: 1,
			},
			Name: name,
		}
	}
	return cluster
}

func newNodeGroup(name string, cluster *clusteroperator.Cluster, properlyOwned bool) *clusteroperator.NodeGroup {
	var controllerReference metav1.OwnerReference
	if properlyOwned {
		var trueVar = true
		controllerReference = metav1.OwnerReference{
			UID:        cluster.UID,
			APIVersion: clusteroperator.SchemeGroupVersion.String(),
			Kind:       "Cluster",
			Name:       cluster.Name,
			Controller: &trueVar,
		}
	}
	return &clusteroperator.NodeGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{controllerReference},
		},
	}
}

func newNodeGroups(store cache.Store, cluster *clusteroperator.Cluster, includeMaster bool, computeNames ...string) []*clusteroperator.NodeGroup {
	nodeGroups := []*clusteroperator.NodeGroup{}
	if includeMaster {
		name := fmt.Sprintf("%s-master-random", cluster.Name)
		nodeGroup := newNodeGroup(name, cluster, true)
		nodeGroup.Spec.NodeType = clusteroperator.NodeTypeMaster
		nodeGroup.Spec.Size = 1
		nodeGroups = append(nodeGroups, nodeGroup)
	}
	for _, clusterNodeGroupName := range computeNames {
		name := fmt.Sprintf("%s-compute-%s-random", cluster.Name, clusterNodeGroupName)
		nodeGroup := newNodeGroup(name, cluster, true)
		nodeGroup.Spec.NodeType = clusteroperator.NodeTypeCompute
		nodeGroup.Spec.Size = 1
		nodeGroups = append(nodeGroups, nodeGroup)
	}
	if store != nil {
		for _, nodeGroup := range nodeGroups {
			store.Add(nodeGroup)
		}
	}
	return nodeGroups
}

// processSync initiates a sync via processNextWorkItem() to test behavior that
// depends on both functions (such as re-queueing on sync error).
func processSync(c *ClusterController, key string) error {
	// Save old syncHandler and replace with one that captures the error.
	oldSyncHandler := c.syncHandler
	defer func() {
		c.syncHandler = oldSyncHandler
	}()
	var syncErr error
	c.syncHandler = func(key string) error {
		syncErr = oldSyncHandler(key)
		return syncErr
	}
	c.queue.Add(key)
	c.processNextWorkItem()
	return syncErr
}

func validateClientActions(t *testing.T, clusterOperatorClient *clusteroperatorclientset.Clientset, expectedActions ...expectedClientAction) {
	actualActions := clusterOperatorClient.Actions()
	if e, a := len(expectedActions), len(actualActions); e != a {
		t.Fatalf("unexpected number of client actions: expected %v, got %v", e, a)
	}
	expectedActionSatisfied := make([]bool, len(expectedActions))
	for _, actualAction := range actualActions {
		actualActionSatisfied := false
		for i, expectedAction := range expectedActions {
			if expectedActionSatisfied[i] {
				continue
			}
			if actualAction.GetResource() != expectedAction.resource() {
				continue
			}
			if actualAction.GetVerb() != expectedAction.verb() {
				continue
			}
			if expectedAction.validate(t, actualAction) {
				actualActionSatisfied = true
				expectedActionSatisfied[i] = true
				break
			}
		}
		if !actualActionSatisfied {
			t.Errorf("Unexpected client action: %+v", actualAction)
		}
	}
}

type expectedClientAction interface {
	resource() schema.GroupVersionResource
	verb() string
	validate(t *testing.T, action clientgotesting.Action) bool
}

type expectedNodeGroupCreateAction struct {
	namePrefix string
}

func (ea expectedNodeGroupCreateAction) resource() schema.GroupVersionResource {
	return clusteroperator.SchemeGroupVersion.WithResource("nodegroups")
}

func (ea expectedNodeGroupCreateAction) verb() string {
	return "create"
}

func (ea expectedNodeGroupCreateAction) validate(t *testing.T, action clientgotesting.Action) bool {
	createAction, ok := action.(clientgotesting.CreateAction)
	if !ok {
		t.Fatalf("create action is not a CreateAction: %t", createAction)
	}
	createdObject := createAction.GetObject()
	nodeGroup, ok := createdObject.(*clusteroperator.NodeGroup)
	if !ok {
		t.Fatalf("node group create action object is not a NodeGroup: %t", nodeGroup)
	}
	return nodeGroup.GenerateName == ea.namePrefix
}

func newExpectedMasterNodeGroupCreateAction(cluster *clusteroperator.Cluster) expectedNodeGroupCreateAction {
	return expectedNodeGroupCreateAction{
		namePrefix: getNamePrefixForMasterNodeGroup(cluster),
	}
}

func newExpectedComputeNodeGroupCreateAction(cluster *clusteroperator.Cluster, computeName string) expectedNodeGroupCreateAction {
	return expectedNodeGroupCreateAction{
		namePrefix: getNamePrefixForComputeNodeGroup(cluster, computeName),
	}
}

type expectedClusterStatusUpdateAction struct {
	masterNodeGroups  int
	computeNodeGroups int
}

func (ea expectedClusterStatusUpdateAction) resource() schema.GroupVersionResource {
	return clusteroperator.SchemeGroupVersion.WithResource("clusters")
}

func (ea expectedClusterStatusUpdateAction) verb() string {
	return "update"
}

func (ea expectedClusterStatusUpdateAction) validate(t *testing.T, action clientgotesting.Action) bool {
	if action.GetSubresource() != "status" {
		return false
	}
	updateAction, ok := action.(clientgotesting.UpdateAction)
	if !ok {
		t.Fatalf("update action is not an UpdateAction: %t", updateAction)
	}
	updatedObject := updateAction.GetObject()
	cluster, ok := updatedObject.(*clusteroperator.Cluster)
	if !ok {
		t.Fatalf("cluster status update action object is not a Cluster: %t", cluster)
	}
	if e, a := ea.masterNodeGroups, cluster.Status.MasterNodeGroups; e != a {
		t.Fatalf("unexpected masterNodeGroups in cluster update status: expected %v, got %v", e, a)
	}
	if e, a := ea.computeNodeGroups, cluster.Status.ComputeNodeGroups; e != a {
		t.Fatalf("unexpected computeNodeGroups in cluster update status: expected %v, got %v", e, a)
	}
	return true
}

func controllerResourceName() string {
	return "clusters"
}

type serverResponse struct {
	statusCode int
	obj        interface{}
}

func TestSyncClusterSteadyState(t *testing.T) {
	cases := []struct {
		name     string
		computes []string
	}{
		{
			name: "only master",
		},
		{
			name:     "single compute",
			computes: []string{"compute1"},
		},
		{
			name:     "multiple computes",
			computes: []string{"compute1", "compute2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			controller, clusterStore, nodeGroupStore, _, clusterOperatorClient := newTestClusterController()

			cluster := newCluster(tc.computes...)
			cluster.Status.MasterNodeGroups = 1
			cluster.Status.ComputeNodeGroups = len(tc.computes)
			clusterStore.Add(cluster)
			newNodeGroups(nodeGroupStore, cluster, true, tc.computes...)

			controller.syncCluster(getKey(cluster, t))

			validateClientActions(t, clusterOperatorClient)
		})
	}
}

func TestSyncClusterCreateNodeGroups(t *testing.T) {
	cases := []struct {
		name             string
		existingMaster   bool
		existingComputes []string
		newComputes      []string
	}{
		{
			name:           "master",
			existingMaster: false,
		},
		{
			name:           "single compute",
			existingMaster: true,
			newComputes:    []string{"compute1"},
		},
		{
			name:           "multiple computes",
			existingMaster: true,
			newComputes:    []string{"compute1", "compute2"},
		},
		{
			name:           "master and computes",
			existingMaster: false,
			newComputes:    []string{"compute1", "compute2"},
		},
		{
			name:             "master with existing computes",
			existingMaster:   false,
			existingComputes: []string{"compute1", "compute2"},
		},
		{
			name:             "additional computes",
			existingMaster:   true,
			existingComputes: []string{"compute1", "compute2"},
			newComputes:      []string{"compute3"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			controller, clusterStore, nodeGroupStore, _, clusterOperatorClient := newTestClusterController()

			cluster := newCluster(append(tc.existingComputes, tc.newComputes...)...)
			if tc.existingMaster {
				cluster.Status.MasterNodeGroups = 1
			}
			cluster.Status.ComputeNodeGroups = len(tc.existingComputes)
			clusterStore.Add(cluster)
			newNodeGroups(nodeGroupStore, cluster, tc.existingMaster, tc.existingComputes...)

			controller.syncCluster(getKey(cluster, t))

			expectedActions := []expectedClientAction{}
			if !tc.existingMaster {
				expectedActions = append(expectedActions, newExpectedMasterNodeGroupCreateAction(cluster))
			}
			for _, newCompute := range tc.newComputes {
				expectedActions = append(expectedActions, newExpectedComputeNodeGroupCreateAction(cluster, newCompute))
			}

			validateClientActions(t, clusterOperatorClient, expectedActions...)
		})
	}
}

func TestSyncClusterNodeGroupsAdded(t *testing.T) {
	cases := []struct {
		name        string
		masterAdded bool
		computes    []string
		oldComputes int
	}{
		{
			name:        "master",
			masterAdded: true,
		},
		{
			name:        "single compute",
			computes:    []string{"compute1"},
			oldComputes: 0,
		},
		{
			name:        "multiple computes",
			computes:    []string{"compute1", "compute2"},
			oldComputes: 0,
		},
		{
			name:        "master and  computes",
			masterAdded: true,
			computes:    []string{"compute1", "compute2"},
			oldComputes: 0,
		},
		{
			name:        "additional computes",
			computes:    []string{"compute1", "compute2", "compute3"},
			oldComputes: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			controller, clusterStore, nodeGroupStore, _, clusterOperatorClient := newTestClusterController()

			cluster := newCluster(tc.computes...)
			if !tc.masterAdded {
				cluster.Status.MasterNodeGroups = 1
			}
			cluster.Status.ComputeNodeGroups = tc.oldComputes
			clusterStore.Add(cluster)
			newNodeGroups(nodeGroupStore, cluster, true, tc.computes...)

			controller.syncCluster(getKey(cluster, t))

			validateClientActions(t, clusterOperatorClient,
				expectedClusterStatusUpdateAction{masterNodeGroups: 1, computeNodeGroups: len(tc.computes)},
			)
		})
	}
}

//func TestPodControllerLookup(t *testing.T) {
//	stopCh := make(chan struct{})
//	defer close(stopCh)
//	manager, informers := testNewReplicaSetControllerFromClient(clientset.NewForConfigOrDie(&restclient.Config{Host: "", ContentConfig: restclient.ContentConfig{GroupVersion: &api.Registry.GroupOrDie(v1.GroupName).GroupVersion}}), stopCh, BurstReplicas)
//	testCases := []struct {
//		inRSs     []*extensions.ReplicaSet
//		pod       *v1.Pod
//		outRSName string
//	}{
//		// pods without labels don't match any ReplicaSets
//		{
//			inRSs: []*extensions.ReplicaSet{
//				{ObjectMeta: metav1.ObjectMeta{Name: "basic"}}},
//			pod:       &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "foo1", Namespace: metav1.NamespaceAll}},
//			outRSName: "",
//		},
//		// Matching labels, not namespace
//		{
//			inRSs: []*extensions.ReplicaSet{
//				{
//					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
//					Spec: extensions.ReplicaSetSpec{
//						Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"foo": "bar"}},
//					},
//				},
//			},
//			pod: &v1.Pod{
//				ObjectMeta: metav1.ObjectMeta{
//					Name: "foo2", Namespace: "ns", Labels: map[string]string{"foo": "bar"}}},
//			outRSName: "",
//		},
//		// Matching ns and labels returns the key to the ReplicaSet, not the ReplicaSet name
//		{
//			inRSs: []*extensions.ReplicaSet{
//				{
//					ObjectMeta: metav1.ObjectMeta{Name: "bar", Namespace: "ns"},
//					Spec: extensions.ReplicaSetSpec{
//						Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"foo": "bar"}},
//					},
//				},
//			},
//			pod: &v1.Pod{
//				ObjectMeta: metav1.ObjectMeta{
//					Name: "foo3", Namespace: "ns", Labels: map[string]string{"foo": "bar"}}},
//			outRSName: "bar",
//		},
//	}
//	for _, c := range testCases {
//		for _, r := range c.inRSs {
//			informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(r)
//		}
//		if rss := manager.getPodReplicaSets(c.pod); rss != nil {
//			if len(rss) != 1 {
//				t.Errorf("len(rss) = %v, want %v", len(rss), 1)
//				continue
//			}
//			rs := rss[0]
//			if c.outRSName != rs.Name {
//				t.Errorf("Got replica set %+v expected %+v", rs.Name, c.outRSName)
//			}
//		} else if c.outRSName != "" {
//			t.Errorf("Expected a replica set %v pod %v, found none", c.outRSName, c.pod.Name)
//		}
//	}
//}

//type FakeWatcher struct {
//	w *watch.FakeWatcher
//	*fake.Clientset
//}

//func TestWatchControllers(t *testing.T) {
//	fakeWatch := watch.NewFake()
//	client := fake.NewSimpleClientset()
//	client.PrependWatchReactor("replicasets", clientgotesting.DefaultWatchReactor(fakeWatch, nil))
//	stopCh := make(chan struct{})
//	defer close(stopCh)
//	informers := informers.NewSharedInformerFactory(client, controller.NoResyncPeriodFunc())
//	manager := NewReplicaSetController(
//		informers.Extensions().V1beta1().ReplicaSets(),
//		informers.Core().V1().Pods(),
//		client,
//		BurstReplicas,
//	)
//	informers.Start(stopCh)

//	var testRSSpec extensions.ReplicaSet
//	received := make(chan string)

//	// The update sent through the fakeWatcher should make its way into the workqueue,
//	// and eventually into the syncHandler. The handler validates the received controller
//	// and closes the received channel to indicate that the test can finish.
//	manager.syncHandler = func(key string) error {
//		obj, exists, err := informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().GetByKey(key)
//		if !exists || err != nil {
//			t.Errorf("Expected to find replica set under key %v", key)
//		}
//		rsSpec := *obj.(*extensions.ReplicaSet)
//		if !apiequality.Semantic.DeepDerivative(rsSpec, testRSSpec) {
//			t.Errorf("Expected %#v, but got %#v", testRSSpec, rsSpec)
//		}
//		close(received)
//		return nil
//	}
//	// Start only the ReplicaSet watcher and the workqueue, send a watch event,
//	// and make sure it hits the sync method.
//	go wait.Until(manager.worker, 10*time.Millisecond, stopCh)

//	testRSSpec.Name = "foo"
//	fakeWatch.Add(&testRSSpec)

//	select {
//	case <-received:
//	case <-time.After(wait.ForeverTestTimeout):
//		t.Errorf("unexpected timeout from result channel")
//	}
//}

//func TestWatchPods(t *testing.T) {
//	client := fake.NewSimpleClientset()

//	fakeWatch := watch.NewFake()
//	client.PrependWatchReactor("pods", clientgotesting.DefaultWatchReactor(fakeWatch, nil))

//	stopCh := make(chan struct{})
//	defer close(stopCh)

//	manager, informers := testNewReplicaSetControllerFromClient(client, stopCh, BurstReplicas)

//	// Put one ReplicaSet into the shared informer
//	labelMap := map[string]string{"foo": "bar"}
//	testRSSpec := newReplicaSet(1, labelMap)
//	informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(testRSSpec)

//	received := make(chan string)
//	// The pod update sent through the fakeWatcher should figure out the managing ReplicaSet and
//	// send it into the syncHandler.
//	manager.syncHandler = func(key string) error {
//		namespace, name, err := cache.SplitMetaNamespaceKey(key)
//		if err != nil {
//			t.Errorf("Error splitting key: %v", err)
//		}
//		rsSpec, err := manager.rsLister.ReplicaSets(namespace).Get(name)
//		if err != nil {
//			t.Errorf("Expected to find replica set under key %v: %v", key, err)
//		}
//		if !apiequality.Semantic.DeepDerivative(rsSpec, testRSSpec) {
//			t.Errorf("\nExpected %#v,\nbut got %#v", testRSSpec, rsSpec)
//		}
//		close(received)
//		return nil
//	}

//	// Start only the pod watcher and the workqueue, send a watch event,
//	// and make sure it hits the sync method for the right ReplicaSet.
//	go informers.Core().V1().Pods().Informer().Run(stopCh)
//	go manager.Run(1, stopCh)

//	pods := newPodList(nil, 1, v1.PodRunning, labelMap, testRSSpec, "pod")
//	testPod := pods.Items[0]
//	testPod.Status.Phase = v1.PodFailed
//	fakeWatch.Add(&testPod)

//	select {
//	case <-received:
//	case <-time.After(wait.ForeverTestTimeout):
//		t.Errorf("unexpected timeout from result channel")
//	}
//}

//func TestUpdatePods(t *testing.T) {
//	stopCh := make(chan struct{})
//	defer close(stopCh)
//	manager, informers := testNewReplicaSetControllerFromClient(fake.NewSimpleClientset(), stopCh, BurstReplicas)

//	received := make(chan string)

//	manager.syncHandler = func(key string) error {
//		namespace, name, err := cache.SplitMetaNamespaceKey(key)
//		if err != nil {
//			t.Errorf("Error splitting key: %v", err)
//		}
//		rsSpec, err := manager.rsLister.ReplicaSets(namespace).Get(name)
//		if err != nil {
//			t.Errorf("Expected to find replica set under key %v: %v", key, err)
//		}
//		received <- rsSpec.Name
//		return nil
//	}

//	go wait.Until(manager.worker, 10*time.Millisecond, stopCh)

//	// Put 2 ReplicaSets and one pod into the informers
//	labelMap1 := map[string]string{"foo": "bar"}
//	testRSSpec1 := newReplicaSet(1, labelMap1)
//	informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(testRSSpec1)
//	testRSSpec2 := *testRSSpec1
//	labelMap2 := map[string]string{"bar": "foo"}
//	testRSSpec2.Spec.Selector = &metav1.LabelSelector{MatchLabels: labelMap2}
//	testRSSpec2.Name = "barfoo"
//	informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(&testRSSpec2)

//	isController := true
//	controllerRef1 := metav1.OwnerReference{UID: testRSSpec1.UID, APIVersion: "v1", Kind: "ReplicaSet", Name: testRSSpec1.Name, Controller: &isController}
//	controllerRef2 := metav1.OwnerReference{UID: testRSSpec2.UID, APIVersion: "v1", Kind: "ReplicaSet", Name: testRSSpec2.Name, Controller: &isController}

//	// case 1: Pod with a ControllerRef
//	pod1 := newPodList(informers.Core().V1().Pods().Informer().GetIndexer(), 1, v1.PodRunning, labelMap1, testRSSpec1, "pod").Items[0]
//	pod1.OwnerReferences = []metav1.OwnerReference{controllerRef1}
//	pod1.ResourceVersion = "1"
//	pod2 := pod1
//	pod2.Labels = labelMap2
//	pod2.ResourceVersion = "2"
//	manager.updatePod(&pod1, &pod2)
//	expected := sets.NewString(testRSSpec1.Name)
//	for _, name := range expected.List() {
//		t.Logf("Expecting update for %+v", name)
//		select {
//		case got := <-received:
//			if !expected.Has(got) {
//				t.Errorf("Expected keys %#v got %v", expected, got)
//			}
//		case <-time.After(wait.ForeverTestTimeout):
//			t.Errorf("Expected update notifications for replica sets")
//		}
//	}

//	// case 2: Remove ControllerRef (orphan). Expect to sync label-matching RS.
//	pod1 = newPodList(informers.Core().V1().Pods().Informer().GetIndexer(), 1, v1.PodRunning, labelMap1, testRSSpec1, "pod").Items[0]
//	pod1.ResourceVersion = "1"
//	pod1.Labels = labelMap2
//	pod1.OwnerReferences = []metav1.OwnerReference{controllerRef2}
//	pod2 = pod1
//	pod2.OwnerReferences = nil
//	pod2.ResourceVersion = "2"
//	manager.updatePod(&pod1, &pod2)
//	expected = sets.NewString(testRSSpec2.Name)
//	for _, name := range expected.List() {
//		t.Logf("Expecting update for %+v", name)
//		select {
//		case got := <-received:
//			if !expected.Has(got) {
//				t.Errorf("Expected keys %#v got %v", expected, got)
//			}
//		case <-time.After(wait.ForeverTestTimeout):
//			t.Errorf("Expected update notifications for replica sets")
//		}
//	}

//	// case 2: Remove ControllerRef (orphan). Expect to sync both former owner and
//	// any label-matching RS.
//	pod1 = newPodList(informers.Core().V1().Pods().Informer().GetIndexer(), 1, v1.PodRunning, labelMap1, testRSSpec1, "pod").Items[0]
//	pod1.ResourceVersion = "1"
//	pod1.Labels = labelMap2
//	pod1.OwnerReferences = []metav1.OwnerReference{controllerRef1}
//	pod2 = pod1
//	pod2.OwnerReferences = nil
//	pod2.ResourceVersion = "2"
//	manager.updatePod(&pod1, &pod2)
//	expected = sets.NewString(testRSSpec1.Name, testRSSpec2.Name)
//	for _, name := range expected.List() {
//		t.Logf("Expecting update for %+v", name)
//		select {
//		case got := <-received:
//			if !expected.Has(got) {
//				t.Errorf("Expected keys %#v got %v", expected, got)
//			}
//		case <-time.After(wait.ForeverTestTimeout):
//			t.Errorf("Expected update notifications for replica sets")
//		}
//	}

//	// case 4: Keep ControllerRef, change labels. Expect to sync owning RS.
//	pod1 = newPodList(informers.Core().V1().Pods().Informer().GetIndexer(), 1, v1.PodRunning, labelMap1, testRSSpec1, "pod").Items[0]
//	pod1.ResourceVersion = "1"
//	pod1.Labels = labelMap1
//	pod1.OwnerReferences = []metav1.OwnerReference{controllerRef2}
//	pod2 = pod1
//	pod2.Labels = labelMap2
//	pod2.ResourceVersion = "2"
//	manager.updatePod(&pod1, &pod2)
//	expected = sets.NewString(testRSSpec2.Name)
//	for _, name := range expected.List() {
//		t.Logf("Expecting update for %+v", name)
//		select {
//		case got := <-received:
//			if !expected.Has(got) {
//				t.Errorf("Expected keys %#v got %v", expected, got)
//			}
//		case <-time.After(wait.ForeverTestTimeout):
//			t.Errorf("Expected update notifications for replica sets")
//		}
//	}
//}

//func TestControllerUpdateRequeue(t *testing.T) {
//	// This server should force a requeue of the controller because it fails to update status.Replicas.
//	labelMap := map[string]string{"foo": "bar"}
//	rs := newReplicaSet(1, labelMap)
//	client := fake.NewSimpleClientset(rs)
//	client.PrependReactor("update", "replicasets",
//		func(action clientgotesting.Action) (bool, runtime.Object, error) {
//			if action.GetSubresource() != "status" {
//				return false, nil, nil
//			}
//			return true, nil, errors.New("failed to update status")
//		})
//	stopCh := make(chan struct{})
//	defer close(stopCh)
//	manager, informers := testNewReplicaSetControllerFromClient(client, stopCh, BurstReplicas)

//	informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(rs)
//	rs.Status = extensions.ReplicaSetStatus{Replicas: 2}
//	newPodList(informers.Core().V1().Pods().Informer().GetIndexer(), 1, v1.PodRunning, labelMap, rs, "pod")

//	fakePodControl := controller.FakePodControl{}
//	manager.podControl = &fakePodControl

//	// Enqueue once. Then process it. Disable rate-limiting for this.
//	manager.queue = workqueue.NewRateLimitingQueue(workqueue.NewMaxOfRateLimiter())
//	manager.enqueueReplicaSet(rs)
//	manager.processNextWorkItem()
//	// It should have been requeued.
//	if got, want := manager.queue.Len(), 1; got != want {
//		t.Errorf("queue.Len() = %v, want %v", got, want)
//	}
//}

//func TestControllerUpdateStatusWithFailure(t *testing.T) {
//	rs := newReplicaSet(1, map[string]string{"foo": "bar"})
//	fakeClient := &fake.Clientset{}
//	fakeClient.AddReactor("get", "replicasets", func(action clientgotesting.Action) (bool, runtime.Object, error) { return true, rs, nil })
//	fakeClient.AddReactor("*", "*", func(action clientgotesting.Action) (bool, runtime.Object, error) {
//		return true, &extensions.ReplicaSet{}, fmt.Errorf("Fake error")
//	})
//	fakeRSClient := fakeClient.Extensions().ReplicaSets("default")
//	numReplicas := int32(10)
//	newStatus := extensions.ReplicaSetStatus{Replicas: numReplicas}
//	updateReplicaSetStatus(fakeRSClient, rs, newStatus)
//	updates, gets := 0, 0
//	for _, a := range fakeClient.Actions() {
//		if a.GetResource().Resource != "replicasets" {
//			t.Errorf("Unexpected action %+v", a)
//			continue
//		}

//		switch action := a.(type) {
//		case clientgotesting.GetAction:
//			gets++
//			// Make sure the get is for the right ReplicaSet even though the update failed.
//			if action.GetName() != rs.Name {
//				t.Errorf("Expected get for ReplicaSet %v, got %+v instead", rs.Name, action.GetName())
//			}
//		case clientgotesting.UpdateAction:
//			updates++
//			// Confirm that the update has the right status.Replicas even though the Get
//			// returned a ReplicaSet with replicas=1.
//			if c, ok := action.GetObject().(*extensions.ReplicaSet); !ok {
//				t.Errorf("Expected a ReplicaSet as the argument to update, got %T", c)
//			} else if c.Status.Replicas != numReplicas {
//				t.Errorf("Expected update for ReplicaSet to contain replicas %v, got %v instead",
//					numReplicas, c.Status.Replicas)
//			}
//		default:
//			t.Errorf("Unexpected action %+v", a)
//			break
//		}
//	}
//	if gets != 1 || updates != 2 {
//		t.Errorf("Expected 1 get and 2 updates, got %d gets %d updates", gets, updates)
//	}
//}

//// TODO: This test is too hairy for a unittest. It should be moved to an E2E suite.
//func doTestControllerBurstReplicas(t *testing.T, burstReplicas, numReplicas int) {
//	labelMap := map[string]string{"foo": "bar"}
//	rsSpec := newReplicaSet(numReplicas, labelMap)
//	client := fake.NewSimpleClientset(rsSpec)
//	fakePodControl := controller.FakePodControl{}
//	stopCh := make(chan struct{})
//	defer close(stopCh)
//	manager, informers := testNewReplicaSetControllerFromClient(client, stopCh, burstReplicas)
//	manager.podControl = &fakePodControl

//	informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(rsSpec)

//	expectedPods := int32(0)
//	pods := newPodList(nil, numReplicas, v1.PodPending, labelMap, rsSpec, "pod")

//	rsKey, err := controller.KeyFunc(rsSpec)
//	if err != nil {
//		t.Errorf("Couldn't get key for object %#v: %v", rsSpec, err)
//	}

//	// Size up the controller, then size it down, and confirm the expected create/delete pattern
//	for _, replicas := range []int32{int32(numReplicas), 0} {

//		*(rsSpec.Spec.Replicas) = replicas
//		informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(rsSpec)

//		for i := 0; i < numReplicas; i += burstReplicas {
//			manager.syncReplicaSet(getKey(rsSpec, t))

//			// The store accrues active pods. It's also used by the ReplicaSet to determine how many
//			// replicas to create.
//			activePods := int32(len(informers.Core().V1().Pods().Informer().GetIndexer().List()))
//			if replicas != 0 {
//				// This is the number of pods currently "in flight". They were created by the
//				// ReplicaSet controller above, which then puts the ReplicaSet to sleep till
//				// all of them have been observed.
//				expectedPods = replicas - activePods
//				if expectedPods > int32(burstReplicas) {
//					expectedPods = int32(burstReplicas)
//				}
//				// This validates the ReplicaSet manager sync actually created pods
//				validateSyncReplicaSet(t, &fakePodControl, int(expectedPods), 0, 0)

//				// This simulates the watch events for all but 1 of the expected pods.
//				// None of these should wake the controller because it has expectations==BurstReplicas.
//				for i := int32(0); i < expectedPods-1; i++ {
//					informers.Core().V1().Pods().Informer().GetIndexer().Add(&pods.Items[i])
//					manager.addPod(&pods.Items[i])
//				}

//				podExp, exists, err := manager.expectations.GetExpectations(rsKey)
//				if !exists || err != nil {
//					t.Fatalf("Did not find expectations for rs.")
//				}
//				if add, _ := podExp.GetExpectations(); add != 1 {
//					t.Fatalf("Expectations are wrong %v", podExp)
//				}
//			} else {
//				expectedPods = (replicas - activePods) * -1
//				if expectedPods > int32(burstReplicas) {
//					expectedPods = int32(burstReplicas)
//				}
//				validateSyncReplicaSet(t, &fakePodControl, 0, int(expectedPods), 0)

//				// To accurately simulate a watch we must delete the exact pods
//				// the rs is waiting for.
//				expectedDels := manager.expectations.GetUIDs(getKey(rsSpec, t))
//				podsToDelete := []*v1.Pod{}
//				isController := true
//				for _, key := range expectedDels.List() {
//					nsName := strings.Split(key, "/")
//					podsToDelete = append(podsToDelete, &v1.Pod{
//						ObjectMeta: metav1.ObjectMeta{
//							Name:      nsName[1],
//							Namespace: nsName[0],
//							Labels:    rsSpec.Spec.Selector.MatchLabels,
//							OwnerReferences: []metav1.OwnerReference{
//								{UID: rsSpec.UID, APIVersion: "v1", Kind: "ReplicaSet", Name: rsSpec.Name, Controller: &isController},
//							},
//						},
//					})
//				}
//				// Don't delete all pods because we confirm that the last pod
//				// has exactly one expectation at the end, to verify that we
//				// don't double delete.
//				for i := range podsToDelete[1:] {
//					informers.Core().V1().Pods().Informer().GetIndexer().Delete(podsToDelete[i])
//					manager.deletePod(podsToDelete[i])
//				}
//				podExp, exists, err := manager.expectations.GetExpectations(rsKey)
//				if !exists || err != nil {
//					t.Fatalf("Did not find expectations for ReplicaSet.")
//				}
//				if _, del := podExp.GetExpectations(); del != 1 {
//					t.Fatalf("Expectations are wrong %v", podExp)
//				}
//			}

//			// Check that the ReplicaSet didn't take any action for all the above pods
//			fakePodControl.Clear()
//			manager.syncReplicaSet(getKey(rsSpec, t))
//			validateSyncReplicaSet(t, &fakePodControl, 0, 0, 0)

//			// Create/Delete the last pod
//			// The last add pod will decrease the expectation of the ReplicaSet to 0,
//			// which will cause it to create/delete the remaining replicas up to burstReplicas.
//			if replicas != 0 {
//				informers.Core().V1().Pods().Informer().GetIndexer().Add(&pods.Items[expectedPods-1])
//				manager.addPod(&pods.Items[expectedPods-1])
//			} else {
//				expectedDel := manager.expectations.GetUIDs(getKey(rsSpec, t))
//				if expectedDel.Len() != 1 {
//					t.Fatalf("Waiting on unexpected number of deletes.")
//				}
//				nsName := strings.Split(expectedDel.List()[0], "/")
//				isController := true
//				lastPod := &v1.Pod{
//					ObjectMeta: metav1.ObjectMeta{
//						Name:      nsName[1],
//						Namespace: nsName[0],
//						Labels:    rsSpec.Spec.Selector.MatchLabels,
//						OwnerReferences: []metav1.OwnerReference{
//							{UID: rsSpec.UID, APIVersion: "v1", Kind: "ReplicaSet", Name: rsSpec.Name, Controller: &isController},
//						},
//					},
//				}
//				informers.Core().V1().Pods().Informer().GetIndexer().Delete(lastPod)
//				manager.deletePod(lastPod)
//			}
//			pods.Items = pods.Items[expectedPods:]
//		}

//		// Confirm that we've created the right number of replicas
//		activePods := int32(len(informers.Core().V1().Pods().Informer().GetIndexer().List()))
//		if activePods != *(rsSpec.Spec.Replicas) {
//			t.Fatalf("Unexpected number of active pods, expected %d, got %d", *(rsSpec.Spec.Replicas), activePods)
//		}
//		// Replenish the pod list, since we cut it down sizing up
//		pods = newPodList(nil, int(replicas), v1.PodRunning, labelMap, rsSpec, "pod")
//	}
//}

//func TestControllerBurstReplicas(t *testing.T) {
//	doTestControllerBurstReplicas(t, 5, 30)
//	doTestControllerBurstReplicas(t, 5, 12)
//	doTestControllerBurstReplicas(t, 3, 2)
//}

//type FakeRSExpectations struct {
//	*controller.ControllerExpectations
//	satisfied    bool
//	expSatisfied func()
//}

//func (fe FakeRSExpectations) SatisfiedExpectations(controllerKey string) bool {
//	fe.expSatisfied()
//	return fe.satisfied
//}

//// TestRSSyncExpectations tests that a pod cannot sneak in between counting active pods
//// and checking expectations.
//func TestRSSyncExpectations(t *testing.T) {
//	client := clientset.NewForConfigOrDie(&restclient.Config{Host: "", ContentConfig: restclient.ContentConfig{GroupVersion: &api.Registry.GroupOrDie(v1.GroupName).GroupVersion}})
//	fakePodControl := controller.FakePodControl{}
//	stopCh := make(chan struct{})
//	defer close(stopCh)
//	manager, informers := testNewReplicaSetControllerFromClient(client, stopCh, 2)
//	manager.podControl = &fakePodControl

//	labelMap := map[string]string{"foo": "bar"}
//	rsSpec := newReplicaSet(2, labelMap)
//	informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(rsSpec)
//	pods := newPodList(nil, 2, v1.PodPending, labelMap, rsSpec, "pod")
//	informers.Core().V1().Pods().Informer().GetIndexer().Add(&pods.Items[0])
//	postExpectationsPod := pods.Items[1]

//	manager.expectations = controller.NewUIDTrackingControllerExpectations(FakeRSExpectations{
//		controller.NewControllerExpectations(), true, func() {
//			// If we check active pods before checking expectataions, the
//			// ReplicaSet will create a new replica because it doesn't see
//			// this pod, but has fulfilled its expectations.
//			informers.Core().V1().Pods().Informer().GetIndexer().Add(&postExpectationsPod)
//		},
//	})
//	manager.syncReplicaSet(getKey(rsSpec, t))
//	validateSyncReplicaSet(t, &fakePodControl, 0, 0, 0)
//}

//func TestDeleteControllerAndExpectations(t *testing.T) {
//	rs := newReplicaSet(1, map[string]string{"foo": "bar"})
//	client := fake.NewSimpleClientset(rs)
//	stopCh := make(chan struct{})
//	defer close(stopCh)
//	manager, informers := testNewReplicaSetControllerFromClient(client, stopCh, 10)

//	informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(rs)

//	fakePodControl := controller.FakePodControl{}
//	manager.podControl = &fakePodControl

//	// This should set expectations for the ReplicaSet
//	manager.syncReplicaSet(getKey(rs, t))
//	validateSyncReplicaSet(t, &fakePodControl, 1, 0, 0)
//	fakePodControl.Clear()

//	// Get the ReplicaSet key
//	rsKey, err := controller.KeyFunc(rs)
//	if err != nil {
//		t.Errorf("Couldn't get key for object %#v: %v", rs, err)
//	}

//	// This is to simulate a concurrent addPod, that has a handle on the expectations
//	// as the controller deletes it.
//	podExp, exists, err := manager.expectations.GetExpectations(rsKey)
//	if !exists || err != nil {
//		t.Errorf("No expectations found for ReplicaSet")
//	}
//	informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Delete(rs)
//	manager.syncReplicaSet(getKey(rs, t))

//	if _, exists, err = manager.expectations.GetExpectations(rsKey); exists {
//		t.Errorf("Found expectaions, expected none since the ReplicaSet has been deleted.")
//	}

//	// This should have no effect, since we've deleted the ReplicaSet.
//	podExp.Add(-1, 0)
//	informers.Core().V1().Pods().Informer().GetIndexer().Replace(make([]interface{}, 0), "0")
//	manager.syncReplicaSet(getKey(rs, t))
//	validateSyncReplicaSet(t, &fakePodControl, 0, 0, 0)
//}

//// shuffle returns a new shuffled list of container controllers.
//func shuffle(controllers []*extensions.ReplicaSet) []*extensions.ReplicaSet {
//	numControllers := len(controllers)
//	randIndexes := rand.Perm(numControllers)
//	shuffled := make([]*extensions.ReplicaSet, numControllers)
//	for i := 0; i < numControllers; i++ {
//		shuffled[i] = controllers[randIndexes[i]]
//	}
//	return shuffled
//}

//func TestOverlappingRSs(t *testing.T) {
//	client := clientset.NewForConfigOrDie(&restclient.Config{Host: "", ContentConfig: restclient.ContentConfig{GroupVersion: &api.Registry.GroupOrDie(v1.GroupName).GroupVersion}})
//	labelMap := map[string]string{"foo": "bar"}

//	stopCh := make(chan struct{})
//	defer close(stopCh)
//	manager, informers := testNewReplicaSetControllerFromClient(client, stopCh, 10)

//	// Create 10 ReplicaSets, shuffled them randomly and insert them into the
//	// ReplicaSet controller's store.
//	// All use the same CreationTimestamp since ControllerRef should be able
//	// to handle that.
//	timestamp := metav1.Date(2014, time.December, 0, 0, 0, 0, 0, time.Local)
//	var controllers []*extensions.ReplicaSet
//	for j := 1; j < 10; j++ {
//		rsSpec := newReplicaSet(1, labelMap)
//		rsSpec.CreationTimestamp = timestamp
//		rsSpec.Name = fmt.Sprintf("rs%d", j)
//		controllers = append(controllers, rsSpec)
//	}
//	shuffledControllers := shuffle(controllers)
//	for j := range shuffledControllers {
//		informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(shuffledControllers[j])
//	}
//	// Add a pod with a ControllerRef and make sure only the corresponding
//	// ReplicaSet is synced. Pick a RS in the middle since the old code used to
//	// sort by name if all timestamps were equal.
//	rs := controllers[3]
//	pods := newPodList(nil, 1, v1.PodPending, labelMap, rs, "pod")
//	pod := &pods.Items[0]
//	isController := true
//	pod.OwnerReferences = []metav1.OwnerReference{
//		{UID: rs.UID, APIVersion: "v1", Kind: "ReplicaSet", Name: rs.Name, Controller: &isController},
//	}
//	rsKey := getKey(rs, t)

//	manager.addPod(pod)
//	queueRS, _ := manager.queue.Get()
//	if queueRS != rsKey {
//		t.Fatalf("Expected to find key %v in queue, found %v", rsKey, queueRS)
//	}
//}

//func TestDeletionTimestamp(t *testing.T) {
//	c := clientset.NewForConfigOrDie(&restclient.Config{Host: "", ContentConfig: restclient.ContentConfig{GroupVersion: &api.Registry.GroupOrDie(v1.GroupName).GroupVersion}})
//	labelMap := map[string]string{"foo": "bar"}
//	stopCh := make(chan struct{})
//	defer close(stopCh)
//	manager, informers := testNewReplicaSetControllerFromClient(c, stopCh, 10)

//	rs := newReplicaSet(1, labelMap)
//	informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(rs)
//	rsKey, err := controller.KeyFunc(rs)
//	if err != nil {
//		t.Errorf("Couldn't get key for object %#v: %v", rs, err)
//	}
//	pod := newPodList(nil, 1, v1.PodPending, labelMap, rs, "pod").Items[0]
//	pod.DeletionTimestamp = &metav1.Time{Time: time.Now()}
//	pod.ResourceVersion = "1"
//	manager.expectations.ExpectDeletions(rsKey, []string{controller.PodKey(&pod)})

//	// A pod added with a deletion timestamp should decrement deletions, not creations.
//	manager.addPod(&pod)

//	queueRS, _ := manager.queue.Get()
//	if queueRS != rsKey {
//		t.Fatalf("Expected to find key %v in queue, found %v", rsKey, queueRS)
//	}
//	manager.queue.Done(rsKey)

//	podExp, exists, err := manager.expectations.GetExpectations(rsKey)
//	if !exists || err != nil || !podExp.Fulfilled() {
//		t.Fatalf("Wrong expectations %#v", podExp)
//	}

//	// An update from no deletion timestamp to having one should be treated
//	// as a deletion.
//	oldPod := newPodList(nil, 1, v1.PodPending, labelMap, rs, "pod").Items[0]
//	oldPod.ResourceVersion = "2"
//	manager.expectations.ExpectDeletions(rsKey, []string{controller.PodKey(&pod)})
//	manager.updatePod(&oldPod, &pod)

//	queueRS, _ = manager.queue.Get()
//	if queueRS != rsKey {
//		t.Fatalf("Expected to find key %v in queue, found %v", rsKey, queueRS)
//	}
//	manager.queue.Done(rsKey)

//	podExp, exists, err = manager.expectations.GetExpectations(rsKey)
//	if !exists || err != nil || !podExp.Fulfilled() {
//		t.Fatalf("Wrong expectations %#v", podExp)
//	}

//	// An update to the pod (including an update to the deletion timestamp)
//	// should not be counted as a second delete.
//	isController := true
//	secondPod := &v1.Pod{
//		ObjectMeta: metav1.ObjectMeta{
//			Namespace: pod.Namespace,
//			Name:      "secondPod",
//			Labels:    pod.Labels,
//			OwnerReferences: []metav1.OwnerReference{
//				{UID: rs.UID, APIVersion: "v1", Kind: "ReplicaSet", Name: rs.Name, Controller: &isController},
//			},
//		},
//	}
//	manager.expectations.ExpectDeletions(rsKey, []string{controller.PodKey(secondPod)})
//	oldPod.DeletionTimestamp = &metav1.Time{Time: time.Now()}
//	oldPod.ResourceVersion = "2"
//	manager.updatePod(&oldPod, &pod)

//	podExp, exists, err = manager.expectations.GetExpectations(rsKey)
//	if !exists || err != nil || podExp.Fulfilled() {
//		t.Fatalf("Wrong expectations %#v", podExp)
//	}

//	// A pod with a non-nil deletion timestamp should also be ignored by the
//	// delete handler, because it's already been counted in the update.
//	manager.deletePod(&pod)
//	podExp, exists, err = manager.expectations.GetExpectations(rsKey)
//	if !exists || err != nil || podExp.Fulfilled() {
//		t.Fatalf("Wrong expectations %#v", podExp)
//	}

//	// Deleting the second pod should clear expectations.
//	manager.deletePod(secondPod)

//	queueRS, _ = manager.queue.Get()
//	if queueRS != rsKey {
//		t.Fatalf("Expected to find key %v in queue, found %v", rsKey, queueRS)
//	}
//	manager.queue.Done(rsKey)

//	podExp, exists, err = manager.expectations.GetExpectations(rsKey)
//	if !exists || err != nil || !podExp.Fulfilled() {
//		t.Fatalf("Wrong expectations %#v", podExp)
//	}
//}

//// setupManagerWithGCEnabled creates a RS manager with a fakePodControl
//func setupManagerWithGCEnabled(stopCh chan struct{}, objs ...runtime.Object) (manager *ReplicaSetController, fakePodControl *controller.FakePodControl, informers informers.SharedInformerFactory) {
//	c := fakeclientset.NewSimpleClientset(objs...)
//	fakePodControl = &controller.FakePodControl{}
//	manager, informers = testNewReplicaSetControllerFromClient(c, stopCh, BurstReplicas)

//	manager.podControl = fakePodControl
//	return manager, fakePodControl, informers
//}

//func TestDoNotPatchPodWithOtherControlRef(t *testing.T) {
//	labelMap := map[string]string{"foo": "bar"}
//	rs := newReplicaSet(2, labelMap)
//	stopCh := make(chan struct{})
//	defer close(stopCh)
//	manager, fakePodControl, informers := setupManagerWithGCEnabled(stopCh, rs)
//	informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(rs)
//	var trueVar = true
//	otherControllerReference := metav1.OwnerReference{UID: uuid.NewUUID(), APIVersion: "v1beta1", Kind: "ReplicaSet", Name: "AnotherRS", Controller: &trueVar}
//	// add to podLister a matching Pod controlled by another controller. Expect no patch.
//	pod := newPod("pod", rs, v1.PodRunning, nil, true)
//	pod.OwnerReferences = []metav1.OwnerReference{otherControllerReference}
//	informers.Core().V1().Pods().Informer().GetIndexer().Add(pod)
//	err := manager.syncReplicaSet(getKey(rs, t))
//	if err != nil {
//		t.Fatal(err)
//	}
//	// because the matching pod already has a controller, so 2 pods should be created.
//	validateSyncReplicaSet(t, fakePodControl, 2, 0, 0)
//}

//func TestPatchPodWithOtherOwnerRef(t *testing.T) {
//	labelMap := map[string]string{"foo": "bar"}
//	rs := newReplicaSet(2, labelMap)
//	stopCh := make(chan struct{})
//	defer close(stopCh)
//	manager, fakePodControl, informers := setupManagerWithGCEnabled(stopCh, rs)
//	informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(rs)
//	// add to podLister one more matching pod that doesn't have a controller
//	// ref, but has an owner ref pointing to other object. Expect a patch to
//	// take control of it.
//	unrelatedOwnerReference := metav1.OwnerReference{UID: uuid.NewUUID(), APIVersion: "batch/v1", Kind: "Job", Name: "Job"}
//	pod := newPod("pod", rs, v1.PodRunning, nil, false)
//	pod.OwnerReferences = []metav1.OwnerReference{unrelatedOwnerReference}
//	informers.Core().V1().Pods().Informer().GetIndexer().Add(pod)

//	err := manager.syncReplicaSet(getKey(rs, t))
//	if err != nil {
//		t.Fatal(err)
//	}
//	// 1 patch to take control of pod, and 1 create of new pod.
//	validateSyncReplicaSet(t, fakePodControl, 1, 0, 1)
//}

//func TestPatchPodWithCorrectOwnerRef(t *testing.T) {
//	labelMap := map[string]string{"foo": "bar"}
//	rs := newReplicaSet(2, labelMap)
//	stopCh := make(chan struct{})
//	defer close(stopCh)
//	manager, fakePodControl, informers := setupManagerWithGCEnabled(stopCh, rs)
//	informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(rs)
//	// add to podLister a matching pod that has an ownerRef pointing to the rs,
//	// but ownerRef.Controller is false. Expect a patch to take control it.
//	rsOwnerReference := metav1.OwnerReference{UID: rs.UID, APIVersion: "v1", Kind: "ReplicaSet", Name: rs.Name}
//	pod := newPod("pod", rs, v1.PodRunning, nil, false)
//	pod.OwnerReferences = []metav1.OwnerReference{rsOwnerReference}
//	informers.Core().V1().Pods().Informer().GetIndexer().Add(pod)

//	err := manager.syncReplicaSet(getKey(rs, t))
//	if err != nil {
//		t.Fatal(err)
//	}
//	// 1 patch to take control of pod, and 1 create of new pod.
//	validateSyncReplicaSet(t, fakePodControl, 1, 0, 1)
//}

//func TestPatchPodFails(t *testing.T) {
//	labelMap := map[string]string{"foo": "bar"}
//	rs := newReplicaSet(2, labelMap)
//	stopCh := make(chan struct{})
//	defer close(stopCh)
//	manager, fakePodControl, informers := setupManagerWithGCEnabled(stopCh, rs)
//	informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(rs)
//	// add to podLister two matching pods. Expect two patches to take control
//	// them.
//	informers.Core().V1().Pods().Informer().GetIndexer().Add(newPod("pod1", rs, v1.PodRunning, nil, false))
//	informers.Core().V1().Pods().Informer().GetIndexer().Add(newPod("pod2", rs, v1.PodRunning, nil, false))
//	// let both patches fail. The rs controller will assume it fails to take
//	// control of the pods and requeue to try again.
//	fakePodControl.Err = fmt.Errorf("Fake Error")
//	rsKey := getKey(rs, t)
//	err := processSync(manager, rsKey)
//	if err == nil || !strings.Contains(err.Error(), "Fake Error") {
//		t.Errorf("expected Fake Error, got %+v", err)
//	}
//	// 2 patches to take control of pod1 and pod2 (both fail).
//	validateSyncReplicaSet(t, fakePodControl, 0, 0, 2)
//	// RS should requeue itself.
//	queueRS, _ := manager.queue.Get()
//	if queueRS != rsKey {
//		t.Fatalf("Expected to find key %v in queue, found %v", rsKey, queueRS)
//	}
//}

//func TestPatchExtraPodsThenDelete(t *testing.T) {
//	labelMap := map[string]string{"foo": "bar"}
//	rs := newReplicaSet(2, labelMap)
//	stopCh := make(chan struct{})
//	defer close(stopCh)
//	manager, fakePodControl, informers := setupManagerWithGCEnabled(stopCh, rs)
//	informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(rs)
//	// add to podLister three matching pods. Expect three patches to take control
//	// them, and later delete one of them.
//	informers.Core().V1().Pods().Informer().GetIndexer().Add(newPod("pod1", rs, v1.PodRunning, nil, false))
//	informers.Core().V1().Pods().Informer().GetIndexer().Add(newPod("pod2", rs, v1.PodRunning, nil, false))
//	informers.Core().V1().Pods().Informer().GetIndexer().Add(newPod("pod3", rs, v1.PodRunning, nil, false))
//	err := manager.syncReplicaSet(getKey(rs, t))
//	if err != nil {
//		t.Fatal(err)
//	}
//	// 3 patches to take control of the pods, and 1 deletion because there is an extra pod.
//	validateSyncReplicaSet(t, fakePodControl, 0, 1, 3)
//}

//func TestUpdateLabelsRemoveControllerRef(t *testing.T) {
//	labelMap := map[string]string{"foo": "bar"}
//	rs := newReplicaSet(2, labelMap)
//	stopCh := make(chan struct{})
//	defer close(stopCh)
//	manager, fakePodControl, informers := setupManagerWithGCEnabled(stopCh, rs)
//	informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(rs)
//	// put one pod in the podLister
//	pod := newPod("pod", rs, v1.PodRunning, nil, false)
//	pod.ResourceVersion = "1"
//	var trueVar = true
//	rsOwnerReference := metav1.OwnerReference{UID: rs.UID, APIVersion: "v1beta1", Kind: "ReplicaSet", Name: rs.Name, Controller: &trueVar}
//	pod.OwnerReferences = []metav1.OwnerReference{rsOwnerReference}
//	updatedPod := *pod
//	// reset the labels
//	updatedPod.Labels = make(map[string]string)
//	updatedPod.ResourceVersion = "2"
//	// add the updatedPod to the store. This is consistent with the behavior of
//	// the Informer: Informer updates the store before call the handler
//	// (updatePod() in this case).
//	informers.Core().V1().Pods().Informer().GetIndexer().Add(&updatedPod)
//	// send a update of the same pod with modified labels
//	manager.updatePod(pod, &updatedPod)
//	// verifies that rs is added to the queue
//	rsKey := getKey(rs, t)
//	queueRS, _ := manager.queue.Get()
//	if queueRS != rsKey {
//		t.Fatalf("Expected to find key %v in queue, found %v", rsKey, queueRS)
//	}
//	manager.queue.Done(queueRS)
//	err := manager.syncReplicaSet(rsKey)
//	if err != nil {
//		t.Fatal(err)
//	}
//	// expect 1 patch to be sent to remove the controllerRef for the pod.
//	// expect 2 creates because the *(rs.Spec.Replicas)=2 and there exists no
//	// matching pod.
//	validateSyncReplicaSet(t, fakePodControl, 2, 0, 1)
//	fakePodControl.Clear()
//}

//func TestUpdateSelectorControllerRef(t *testing.T) {
//	labelMap := map[string]string{"foo": "bar"}
//	rs := newReplicaSet(2, labelMap)
//	stopCh := make(chan struct{})
//	defer close(stopCh)
//	manager, fakePodControl, informers := setupManagerWithGCEnabled(stopCh, rs)
//	// put 2 pods in the podLister
//	newPodList(informers.Core().V1().Pods().Informer().GetIndexer(), 2, v1.PodRunning, labelMap, rs, "pod")
//	// update the RS so that its selector no longer matches the pods
//	updatedRS := *rs
//	updatedRS.Spec.Selector.MatchLabels = map[string]string{"foo": "baz"}
//	// put the updatedRS into the store. This is consistent with the behavior of
//	// the Informer: Informer updates the store before call the handler
//	// (updateRS() in this case).
//	informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(&updatedRS)
//	manager.updateRS(rs, &updatedRS)
//	// verifies that the rs is added to the queue
//	rsKey := getKey(rs, t)
//	queueRS, _ := manager.queue.Get()
//	if queueRS != rsKey {
//		t.Fatalf("Expected to find key %v in queue, found %v", rsKey, queueRS)
//	}
//	manager.queue.Done(queueRS)
//	err := manager.syncReplicaSet(rsKey)
//	if err != nil {
//		t.Fatal(err)
//	}
//	// expect 2 patches to be sent to remove the controllerRef for the pods.
//	// expect 2 creates because the *(rc.Spec.Replicas)=2 and there exists no
//	// matching pod.
//	validateSyncReplicaSet(t, fakePodControl, 2, 0, 2)
//	fakePodControl.Clear()
//}

//// RS controller shouldn't adopt or create more pods if the rc is about to be
//// deleted.
//func TestDoNotAdoptOrCreateIfBeingDeleted(t *testing.T) {
//	labelMap := map[string]string{"foo": "bar"}
//	rs := newReplicaSet(2, labelMap)
//	now := metav1.Now()
//	rs.DeletionTimestamp = &now
//	stopCh := make(chan struct{})
//	defer close(stopCh)
//	manager, fakePodControl, informers := setupManagerWithGCEnabled(stopCh, rs)
//	informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(rs)
//	pod1 := newPod("pod1", rs, v1.PodRunning, nil, false)
//	informers.Core().V1().Pods().Informer().GetIndexer().Add(pod1)

//	// no patch, no create
//	err := manager.syncReplicaSet(getKey(rs, t))
//	if err != nil {
//		t.Fatal(err)
//	}
//	validateSyncReplicaSet(t, fakePodControl, 0, 0, 0)
//}

//func TestDoNotAdoptOrCreateIfBeingDeletedRace(t *testing.T) {
//	labelMap := map[string]string{"foo": "bar"}
//	// Bare client says it IS deleted.
//	rs := newReplicaSet(2, labelMap)
//	now := metav1.Now()
//	rs.DeletionTimestamp = &now
//	stopCh := make(chan struct{})
//	defer close(stopCh)
//	manager, fakePodControl, informers := setupManagerWithGCEnabled(stopCh, rs)
//	// Lister (cache) says it's NOT deleted.
//	rs2 := *rs
//	rs2.DeletionTimestamp = nil
//	informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(&rs2)

//	// Recheck occurs if a matching orphan is present.
//	pod1 := newPod("pod1", rs, v1.PodRunning, nil, false)
//	informers.Core().V1().Pods().Informer().GetIndexer().Add(pod1)

//	// sync should abort.
//	err := manager.syncReplicaSet(getKey(rs, t))
//	if err == nil {
//		t.Error("syncReplicaSet() err = nil, expected non-nil")
//	}
//	// no patch, no create.
//	validateSyncReplicaSet(t, fakePodControl, 0, 0, 0)
//}

//func TestReadyReplicas(t *testing.T) {
//	// This is a happy server just to record the PUT request we expect for status.Replicas
//	fakeHandler := utiltesting.FakeHandler{
//		StatusCode:    200,
//		ResponseBody:  "{}",
//		SkipRequestFn: skipListerFunc,
//	}
//	testServer := httptest.NewServer(&fakeHandler)
//	defer testServer.Close()

//	client := clientset.NewForConfigOrDie(&restclient.Config{Host: testServer.URL, ContentConfig: restclient.ContentConfig{GroupVersion: &api.Registry.GroupOrDie(v1.GroupName).GroupVersion}})
//	stopCh := make(chan struct{})
//	defer close(stopCh)
//	manager, informers := testNewReplicaSetControllerFromClient(client, stopCh, BurstReplicas)

//	// Status.Replica should update to match number of pods in system, 1 new pod should be created.
//	labelMap := map[string]string{"foo": "bar"}
//	rs := newReplicaSet(2, labelMap)
//	rs.Status = extensions.ReplicaSetStatus{Replicas: 2, ReadyReplicas: 0, AvailableReplicas: 0, ObservedGeneration: 1}
//	rs.Generation = 1
//	informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(rs)

//	newPodList(informers.Core().V1().Pods().Informer().GetIndexer(), 2, v1.PodPending, labelMap, rs, "pod")
//	newPodList(informers.Core().V1().Pods().Informer().GetIndexer(), 2, v1.PodRunning, labelMap, rs, "pod")

//	// This response body is just so we don't err out decoding the http response
//	response := runtime.EncodeOrDie(testapi.Extensions.Codec(), &extensions.ReplicaSet{})
//	fakeHandler.SetResponseBody(response)

//	fakePodControl := controller.FakePodControl{}
//	manager.podControl = &fakePodControl

//	manager.syncReplicaSet(getKey(rs, t))

//	// ReadyReplicas should go from 0 to 2.
//	rs.Status = extensions.ReplicaSetStatus{Replicas: 2, ReadyReplicas: 2, AvailableReplicas: 2, ObservedGeneration: 1}

//	decRs := runtime.EncodeOrDie(testapi.Extensions.Codec(), rs)
//	fakeHandler.ValidateRequest(t, testapi.Extensions.ResourcePath(replicaSetResourceName(), rs.Namespace, rs.Name)+"/status", "PUT", &decRs)
//	validateSyncReplicaSet(t, &fakePodControl, 0, 0, 0)
//}

//func TestAvailableReplicas(t *testing.T) {
//	// This is a happy server just to record the PUT request we expect for status.Replicas
//	fakeHandler := utiltesting.FakeHandler{
//		StatusCode:    200,
//		ResponseBody:  "{}",
//		SkipRequestFn: skipListerFunc,
//	}
//	testServer := httptest.NewServer(&fakeHandler)
//	defer testServer.Close()

//	client := clientset.NewForConfigOrDie(&restclient.Config{Host: testServer.URL, ContentConfig: restclient.ContentConfig{GroupVersion: &api.Registry.GroupOrDie(v1.GroupName).GroupVersion}})
//	stopCh := make(chan struct{})
//	defer close(stopCh)
//	manager, informers := testNewReplicaSetControllerFromClient(client, stopCh, BurstReplicas)

//	// Status.Replica should update to match number of pods in system, 1 new pod should be created.
//	labelMap := map[string]string{"foo": "bar"}
//	rs := newReplicaSet(2, labelMap)
//	rs.Status = extensions.ReplicaSetStatus{Replicas: 2, ReadyReplicas: 0, AvailableReplicas: 0, ObservedGeneration: 1}
//	rs.Generation = 1
//	// minReadySeconds set to 15s
//	rs.Spec.MinReadySeconds = 15
//	informers.Extensions().V1beta1().ReplicaSets().Informer().GetIndexer().Add(rs)

//	// First pod becomes ready 20s ago
//	moment := metav1.Time{Time: time.Now().Add(-2e10)}
//	pod := newPod("pod", rs, v1.PodRunning, &moment, true)
//	informers.Core().V1().Pods().Informer().GetIndexer().Add(pod)

//	// Second pod becomes ready now
//	otherMoment := metav1.Now()
//	otherPod := newPod("otherPod", rs, v1.PodRunning, &otherMoment, true)
//	informers.Core().V1().Pods().Informer().GetIndexer().Add(otherPod)

//	// This response body is just so we don't err out decoding the http response
//	response := runtime.EncodeOrDie(testapi.Extensions.Codec(), &extensions.ReplicaSet{})
//	fakeHandler.SetResponseBody(response)

//	fakePodControl := controller.FakePodControl{}
//	manager.podControl = &fakePodControl

//	// The controller should see only one available pod.
//	manager.syncReplicaSet(getKey(rs, t))

//	rs.Status = extensions.ReplicaSetStatus{Replicas: 2, ReadyReplicas: 2, AvailableReplicas: 1, ObservedGeneration: 1}

//	decRs := runtime.EncodeOrDie(testapi.Extensions.Codec(), rs)
//	fakeHandler.ValidateRequest(t, testapi.Extensions.ResourcePath(replicaSetResourceName(), rs.Namespace, rs.Name)+"/status", "PUT", &decRs)
//	validateSyncReplicaSet(t, &fakePodControl, 0, 0, 0)
//}

//var (
//	imagePullBackOff extensions.ReplicaSetConditionType = "ImagePullBackOff"

//	condImagePullBackOff = func() extensions.ReplicaSetCondition {
//		return extensions.ReplicaSetCondition{
//			Type:   imagePullBackOff,
//			Status: v1.ConditionTrue,
//			Reason: "NonExistentImage",
//		}
//	}

//	condReplicaFailure = func() extensions.ReplicaSetCondition {
//		return extensions.ReplicaSetCondition{
//			Type:   extensions.ReplicaSetReplicaFailure,
//			Status: v1.ConditionTrue,
//			Reason: "OtherFailure",
//		}
//	}

//	condReplicaFailure2 = func() extensions.ReplicaSetCondition {
//		return extensions.ReplicaSetCondition{
//			Type:   extensions.ReplicaSetReplicaFailure,
//			Status: v1.ConditionTrue,
//			Reason: "AnotherFailure",
//		}
//	}

//	status = func() *extensions.ReplicaSetStatus {
//		return &extensions.ReplicaSetStatus{
//			Conditions: []extensions.ReplicaSetCondition{condReplicaFailure()},
//		}
//	}
//)

//func TestGetCondition(t *testing.T) {
//	exampleStatus := status()

//	tests := []struct {
//		name string

//		status     extensions.ReplicaSetStatus
//		condType   extensions.ReplicaSetConditionType
//		condStatus v1.ConditionStatus
//		condReason string

//		expected bool
//	}{
//		{
//			name: "condition exists",

//			status:   *exampleStatus,
//			condType: extensions.ReplicaSetReplicaFailure,

//			expected: true,
//		},
//		{
//			name: "condition does not exist",

//			status:   *exampleStatus,
//			condType: imagePullBackOff,

//			expected: false,
//		},
//	}

//	for _, test := range tests {
//		cond := GetCondition(test.status, test.condType)
//		exists := cond != nil
//		if exists != test.expected {
//			t.Errorf("%s: expected condition to exist: %t, got: %t", test.name, test.expected, exists)
//		}
//	}
//}

//func TestSetCondition(t *testing.T) {
//	tests := []struct {
//		name string

//		status *extensions.ReplicaSetStatus
//		cond   extensions.ReplicaSetCondition

//		expectedStatus *extensions.ReplicaSetStatus
//	}{
//		{
//			name: "set for the first time",

//			status: &extensions.ReplicaSetStatus{},
//			cond:   condReplicaFailure(),

//			expectedStatus: &extensions.ReplicaSetStatus{Conditions: []extensions.ReplicaSetCondition{condReplicaFailure()}},
//		},
//		{
//			name: "simple set",

//			status: &extensions.ReplicaSetStatus{Conditions: []extensions.ReplicaSetCondition{condImagePullBackOff()}},
//			cond:   condReplicaFailure(),

//			expectedStatus: &extensions.ReplicaSetStatus{Conditions: []extensions.ReplicaSetCondition{condImagePullBackOff(), condReplicaFailure()}},
//		},
//		{
//			name: "overwrite",

//			status: &extensions.ReplicaSetStatus{Conditions: []extensions.ReplicaSetCondition{condReplicaFailure()}},
//			cond:   condReplicaFailure2(),

//			expectedStatus: &extensions.ReplicaSetStatus{Conditions: []extensions.ReplicaSetCondition{condReplicaFailure2()}},
//		},
//	}

//	for _, test := range tests {
//		SetCondition(test.status, test.cond)
//		if !reflect.DeepEqual(test.status, test.expectedStatus) {
//			t.Errorf("%s: expected status: %v, got: %v", test.name, test.expectedStatus, test.status)
//		}
//	}
//}

//func TestRemoveCondition(t *testing.T) {
//	tests := []struct {
//		name string

//		status   *extensions.ReplicaSetStatus
//		condType extensions.ReplicaSetConditionType

//		expectedStatus *extensions.ReplicaSetStatus
//	}{
//		{
//			name: "remove from empty status",

//			status:   &extensions.ReplicaSetStatus{},
//			condType: extensions.ReplicaSetReplicaFailure,

//			expectedStatus: &extensions.ReplicaSetStatus{},
//		},
//		{
//			name: "simple remove",

//			status:   &extensions.ReplicaSetStatus{Conditions: []extensions.ReplicaSetCondition{condReplicaFailure()}},
//			condType: extensions.ReplicaSetReplicaFailure,

//			expectedStatus: &extensions.ReplicaSetStatus{},
//		},
//		{
//			name: "doesn't remove anything",

//			status:   status(),
//			condType: imagePullBackOff,

//			expectedStatus: status(),
//		},
//	}

//	for _, test := range tests {
//		RemoveCondition(test.status, test.condType)
//		if !reflect.DeepEqual(test.status, test.expectedStatus) {
//			t.Errorf("%s: expected status: %v, got: %v", test.name, test.expectedStatus, test.status)
//		}
//	}
//}

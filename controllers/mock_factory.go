// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	r "github.com/inditextech/redkeyoperator/internal/redis"
	"github.com/inditextech/redkeyoperator/internal/robin"

	ginkgo "github.com/onsi/ginkgo/v2"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"strconv"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newClient(rc *redkeyv1.RedkeyCluster, s *runtime.Scheme) client.Client {
	return fake.NewClientBuilder().WithObjects(rc).WithScheme(s).Build()
}

func newScheme() *runtime.Scheme {
	sb := redkeyv1.SchemeBuilder
	s, err := sb.Build()
	if err != nil {
		fmt.Println(err)
	}
	return s
}

func newReconciler(redis *redkeyv1.RedkeyCluster, recorder record.EventRecorder) *RedkeyClusterReconciler {
	ctrl.SetLogger(zap.New(zap.WriteTo(ginkgo.GinkgoWriter), zap.UseDevMode(true)))

	var defaultPrimaries int32 = 3

	scheme := newScheme()

	reconciler := &RedkeyClusterReconciler{
		Client:                      newClient(redis, scheme),
		Scheme:                      scheme,
		Log:                         ctrl.Log.WithName("controllers").WithName("redkeycluster"),
		MaxConcurrentReconciles:     10,
		ConcurrentMigrate:           3,
		Recorder:                    recorder,
		GetReadyNodesFunc:           mockReadyNodes(make(map[string]*redkeyv1.RedisNode)),
		FindExistingStatefulSetFunc: mockStatefulSet(newStatefulSet(redis, defaultPrimaries)),
		FindExistingConfigMapFunc:   mockConfigMap(newConfigMap()),
		NewRobinFunc:                mockNewRobin(&MockRobinClient{}),
	}

	return reconciler
}

func newContext() context.Context {
	return context.Background()
}

func newRequest(rc *redkeyv1.RedkeyCluster) ctrl.Request {
	return ctrl.Request{
		NamespacedName: types.NamespacedName{
			Namespace: rc.Namespace,
			Name:      rc.Name,
		},
	}
}

func newRedkeyCluster() *redkeyv1.RedkeyCluster {
	om := metav1.ObjectMeta{
		Name:      "redis-cluster",
		Namespace: "unittest",
		Labels: map[string]string{
			"label-key": "label-value",
		},
	}
	return &redkeyv1.RedkeyCluster{
		ObjectMeta: om,
		Status: redkeyv1.RedkeyClusterStatus{
			Status:     redkeyv1.StatusReady,
			Conditions: []metav1.Condition{},
		},
		Spec: redkeyv1.RedkeyClusterSpec{
			Primaries: 3,
			Config:    r.MapToConfigString(r.MergeWithDefaultConfig(nil, false, 0)),
			Image:     "redkey-operator:0.3.0",
			Resources: &corev1.ResourceRequirements{
				Limits:   newLimits(),
				Requests: newRequests(),
			},
		},
	}
}

func newRequests() corev1.ResourceList {
	return corev1.ResourceList{}
}

func newLimits() corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("1"),
		corev1.ResourceMemory: resource.MustParse("16Gi"),
	}
}

func mockReadyNodes(nodes map[string]*redkeyv1.RedisNode) func(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster) (map[string]*redkeyv1.RedisNode, error) {
	return func(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster) (map[string]*redkeyv1.RedisNode, error) {
		return nodes, nil
	}
}

func newReadyNodes(amount int) map[string]*redkeyv1.RedisNode {
	readyNodes := make(map[string]*redkeyv1.RedisNode)
	for i := 0; i < amount; i++ {
		readyNodes[strconv.Itoa(i)] = &redkeyv1.RedisNode{}
	}
	return readyNodes
}

func mockStatefulSet(sset *v1.StatefulSet) func(ctx context.Context, req ctrl.Request) (*v1.StatefulSet, error) {
	return func(ctx context.Context, req ctrl.Request) (*v1.StatefulSet, error) {
		return sset, nil
	}
}

func newStatefulSet(redis *redkeyv1.RedkeyCluster, numReplicas int32) *v1.StatefulSet {
	req := newRequest(redis)
	spec := redis.Spec
	labels := make(map[string]string)
	sset, _ := r.CreateStatefulSet(newContext(), req, spec, labels)
	sset.Spec.Replicas = &numReplicas
	for k := range sset.Spec.Template.Spec.Containers {
		sset.Spec.Template.Spec.Containers[k].Resources.Limits[corev1.ResourceCPU] = *redis.Spec.Resources.Limits.Cpu()
		sset.Spec.Template.Spec.Containers[k].Resources.Limits[corev1.ResourceMemory] = *redis.Spec.Resources.Limits.Memory()
		sset.Spec.Template.Spec.Containers[k].Resources.Requests[corev1.ResourceCPU] = *redis.Spec.Resources.Requests.Cpu()
		sset.Spec.Template.Spec.Containers[k].Resources.Requests[corev1.ResourceMemory] = *redis.Spec.Resources.Requests.Memory()
	}
	return sset
}

func mockConfigMap(configMap *corev1.ConfigMap) func(ctx context.Context, req ctrl.Request) (*corev1.ConfigMap, error) {
	return func(ctx context.Context, req ctrl.Request) (*corev1.ConfigMap, error) {
		return configMap, nil
	}
}

func newConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{}
}

// MockRobinClient implements robin.RobinClient for testing.
type MockRobinClient struct {
	StatusValue        string
	ClusterStatusValue string
	Primaries          int
	ReplicasPerPrimary int
	ClusterNodesValue  robin.ClusterNodes
	ClusterCheckOk     bool
	ClusterCheckErrors []string
	ClusterCheckWarns  []string
	MoveSlotsCompleted bool
	Err                error
}

func (m *MockRobinClient) GetStatus(ctx context.Context) (string, error) {
	return m.StatusValue, m.Err
}

func (m *MockRobinClient) SetStatus(ctx context.Context, status string) error {
	m.StatusValue = status
	return m.Err
}

func (m *MockRobinClient) SetAndPersistRobinStatus(ctx context.Context, client client.Client, redkeyCluster *redkeyv1.RedkeyCluster, newStatus string) error {
	m.StatusValue = newStatus
	return m.Err
}

func (m *MockRobinClient) GetReplicas(ctx context.Context) (int, int, error) {
	return m.Primaries, m.ReplicasPerPrimary, m.Err
}

func (m *MockRobinClient) SetReplicas(ctx context.Context, clusterReplicas int, clusterReplicasPerPrimary int) error {
	m.Primaries = clusterReplicas
	m.ReplicasPerPrimary = clusterReplicasPerPrimary
	return m.Err
}

func (m *MockRobinClient) ClusterCheck(ctx context.Context) (bool, []string, []string, error) {
	return m.ClusterCheckOk, m.ClusterCheckErrors, m.ClusterCheckWarns, m.Err
}

func (m *MockRobinClient) GetClusterNodes(ctx context.Context) (robin.ClusterNodes, error) {
	return m.ClusterNodesValue, m.Err
}

func (m *MockRobinClient) ClusterFix(ctx context.Context) error {
	return m.Err
}

func (m *MockRobinClient) ClusterResetNode(ctx context.Context, nodeIndex int) error {
	return m.Err
}

func (m *MockRobinClient) MoveSlots(ctx context.Context, nodeIndexFrom int, nodeIndexTo int, numSlots int) (bool, error) {
	return m.MoveSlotsCompleted, m.Err
}

func (m *MockRobinClient) ClusterRecreate(ctx context.Context) error {
	return m.Err
}

func (m *MockRobinClient) GetClusterStatus(ctx context.Context) (string, error) {
	return m.ClusterStatusValue, m.Err
}

func (m *MockRobinClient) GetPod() *corev1.Pod {
	return &corev1.Pod{}
}

func mockNewRobin(mock robin.RobinClient) func(ctx context.Context, client client.Client, redkeyCluster *redkeyv1.RedkeyCluster, logger logr.Logger) (robin.RobinClient, error) {
	return func(ctx context.Context, client client.Client, redkeyCluster *redkeyv1.RedkeyCluster, logger logr.Logger) (robin.RobinClient, error) {
		return mock, nil
	}
}

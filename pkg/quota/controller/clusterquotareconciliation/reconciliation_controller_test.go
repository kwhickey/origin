package clusterquotareconciliation

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	kapi "k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/resource"
	"k8s.io/kubernetes/pkg/api/unversioned"
	ktestclient "k8s.io/kubernetes/pkg/client/unversioned/testclient"
	utilquota "k8s.io/kubernetes/pkg/quota"
	utildiff "k8s.io/kubernetes/pkg/util/diff"
	"k8s.io/kubernetes/pkg/util/sets"

	"github.com/openshift/origin/pkg/client/testclient"
	quotaapi "github.com/openshift/origin/pkg/quota/api"
	quotaapiv1 "github.com/openshift/origin/pkg/quota/api/v1"
	"github.com/openshift/origin/pkg/quota/controller/clusterquotamapping"
)

func defaultQuota() *quotaapi.ClusterResourceQuota {
	return &quotaapi.ClusterResourceQuota{
		ObjectMeta: kapi.ObjectMeta{Name: "foo"},
		Spec: quotaapi.ClusterResourceQuotaSpec{
			Quota: kapi.ResourceQuotaSpec{
				Hard: kapi.ResourceList{
					kapi.ResourcePods:    resource.MustParse("10"),
					kapi.ResourceSecrets: resource.MustParse("5"),
				},
			},
		},
	}
}

func TestSyncFunc(t *testing.T) {
	testCases := []struct {
		name            string
		startingQuota   func() *quotaapi.ClusterResourceQuota
		workItems       []workItem
		mapperFunc      func() clusterquotamapping.ClusterQuotaMapper
		calculationFunc func(namespaceName string, scopes []kapi.ResourceQuotaScope, hardLimits kapi.ResourceList, registry utilquota.Registry) (kapi.ResourceList, error)

		expectedQuota   func() *quotaapi.ClusterResourceQuota
		expectedRetries []workItem
		expectedError   string
	}{
		{
			name:          "from nothing",
			startingQuota: defaultQuota,
			workItems: []workItem{
				{namespaceName: "one"},
			},
			mapperFunc: func() clusterquotamapping.ClusterQuotaMapper {
				mapper := newFakeClusterQuotaMapper()
				mapper.quotaToNamespaces["foo"] = sets.NewString("one")
				return mapper
			},
			calculationFunc: func(namespaceName string, scopes []kapi.ResourceQuotaScope, hardLimits kapi.ResourceList, registry utilquota.Registry) (kapi.ResourceList, error) {
				if e, a := "one", namespaceName; e != a {
					t.Errorf("%s: expected %v, got %v", "from nothing", e, a)
				}
				ret := kapi.ResourceList{}
				ret[kapi.ResourcePods] = resource.MustParse("10")
				return ret, nil
			},
			expectedQuota: func() *quotaapi.ClusterResourceQuota {
				ret := defaultQuota()
				ret.Status.Total.Hard = ret.Spec.Quota.Hard
				ret.Status.Total.Used = kapi.ResourceList{kapi.ResourcePods: resource.MustParse("10")}
				ret.Status.Namespaces.Insert("one", kapi.ResourceQuotaStatus{
					Hard: ret.Spec.Quota.Hard,
					Used: kapi.ResourceList{kapi.ResourcePods: resource.MustParse("10")},
				})
				return ret
			},
			expectedRetries: []workItem{},
		},
		{
			name:          "cache not ready",
			startingQuota: defaultQuota,
			workItems: []workItem{
				{namespaceName: "one"},
			},
			mapperFunc: func() clusterquotamapping.ClusterQuotaMapper {
				mapper := newFakeClusterQuotaMapper()
				mapper.quotaToNamespaces["foo"] = sets.NewString("one")
				mapper.quotaToSelector["foo"] = &unversioned.LabelSelector{}
				return mapper
			},
			calculationFunc: func(namespaceName string, scopes []kapi.ResourceQuotaScope, hardLimits kapi.ResourceList, registry utilquota.Registry) (kapi.ResourceList, error) {
				t.Errorf("%s: shouldn't be called", "cache not ready")
				return nil, nil
			},
			expectedQuota: func() *quotaapi.ClusterResourceQuota {
				return nil
			},
			expectedRetries: []workItem{
				{namespaceName: "one"},
			},
			expectedError: "mapping not up to date",
		},
		{
			name:          "removed from nothing",
			startingQuota: defaultQuota,
			workItems: []workItem{
				{namespaceName: "one"},
			},
			mapperFunc: func() clusterquotamapping.ClusterQuotaMapper {
				return newFakeClusterQuotaMapper()
			},
			calculationFunc: func(namespaceName string, scopes []kapi.ResourceQuotaScope, hardLimits kapi.ResourceList, registry utilquota.Registry) (kapi.ResourceList, error) {
				if e, a := "one", namespaceName; e != a {
					t.Errorf("%s: expected %v, got %v", "removed from nothing", e, a)
				}
				ret := kapi.ResourceList{}
				ret[kapi.ResourcePods] = resource.MustParse("10")
				return ret, nil
			},
			expectedQuota: func() *quotaapi.ClusterResourceQuota {
				ret := defaultQuota()
				ret.Status.Total.Hard = ret.Spec.Quota.Hard
				ret.Status.Total.Used = kapi.ResourceList{}
				return ret
			},
			expectedRetries: []workItem{},
		},
		{
			name: "removed from something",
			startingQuota: func() *quotaapi.ClusterResourceQuota {
				ret := defaultQuota()
				ret.Status.Total.Hard = ret.Spec.Quota.Hard
				ret.Status.Total.Used = kapi.ResourceList{kapi.ResourcePods: resource.MustParse("10")}
				ret.Status.Namespaces.Insert("one", kapi.ResourceQuotaStatus{
					Hard: ret.Spec.Quota.Hard,
					Used: kapi.ResourceList{kapi.ResourcePods: resource.MustParse("10")},
				})
				return ret
			},
			workItems: []workItem{
				{namespaceName: "one"},
			},
			mapperFunc: func() clusterquotamapping.ClusterQuotaMapper {
				return newFakeClusterQuotaMapper()
			},
			calculationFunc: func(namespaceName string, scopes []kapi.ResourceQuotaScope, hardLimits kapi.ResourceList, registry utilquota.Registry) (kapi.ResourceList, error) {
				if e, a := "one", namespaceName; e != a {
					t.Errorf("%s: expected %v, got %v", "removed from something", e, a)
				}
				ret := kapi.ResourceList{}
				ret[kapi.ResourcePods] = resource.MustParse("10")
				return ret, nil
			},
			expectedQuota: func() *quotaapi.ClusterResourceQuota {
				ret := defaultQuota()
				ret.Status.Total.Hard = ret.Spec.Quota.Hard
				ret.Status.Total.Used = kapi.ResourceList{kapi.ResourcePods: resource.MustParse("0")}
				return ret
			},
			expectedRetries: []workItem{},
		},
		{
			name: "update one, remove two, ignore three, fail four",
			startingQuota: func() *quotaapi.ClusterResourceQuota {
				ret := defaultQuota()
				ret.Status.Total.Hard = ret.Spec.Quota.Hard
				ret.Status.Total.Used = kapi.ResourceList{kapi.ResourcePods: resource.MustParse("30")}
				ret.Status.Namespaces.Insert("one", kapi.ResourceQuotaStatus{
					Hard: ret.Spec.Quota.Hard,
					Used: kapi.ResourceList{kapi.ResourcePods: resource.MustParse("5")},
				})
				ret.Status.Namespaces.Insert("two", kapi.ResourceQuotaStatus{
					Hard: ret.Spec.Quota.Hard,
					Used: kapi.ResourceList{kapi.ResourcePods: resource.MustParse("10")},
				})
				ret.Status.Namespaces.Insert("three", kapi.ResourceQuotaStatus{
					Hard: ret.Spec.Quota.Hard,
					Used: kapi.ResourceList{kapi.ResourcePods: resource.MustParse("15")},
				})
				return ret
			},
			workItems: []workItem{
				{namespaceName: "one", forceRecalculation: true},
				{namespaceName: "two"},
				{namespaceName: "three"},
				{namespaceName: "four"},
			},
			mapperFunc: func() clusterquotamapping.ClusterQuotaMapper {
				mapper := newFakeClusterQuotaMapper()
				mapper.quotaToNamespaces["foo"] = sets.NewString("one", "three", "four")
				return mapper
			},
			calculationFunc: func(namespaceName string, scopes []kapi.ResourceQuotaScope, hardLimits kapi.ResourceList, registry utilquota.Registry) (kapi.ResourceList, error) {
				if namespaceName == "four" {
					return nil, fmt.Errorf("calculation error")
				}
				ret := kapi.ResourceList{}
				ret[kapi.ResourcePods] = resource.MustParse("10")
				return ret, nil
			},
			expectedQuota: func() *quotaapi.ClusterResourceQuota {
				ret := defaultQuota()
				ret.Status.Total.Hard = ret.Spec.Quota.Hard
				ret.Status.Total.Used = kapi.ResourceList{kapi.ResourcePods: resource.MustParse("25")}
				ret.Status.Namespaces.Insert("one", kapi.ResourceQuotaStatus{
					Hard: ret.Spec.Quota.Hard,
					Used: kapi.ResourceList{kapi.ResourcePods: resource.MustParse("10")},
				})
				ret.Status.Namespaces.Insert("three", kapi.ResourceQuotaStatus{
					Hard: ret.Spec.Quota.Hard,
					Used: kapi.ResourceList{kapi.ResourcePods: resource.MustParse("15")},
				})
				return ret
			},
			expectedRetries: []workItem{{namespaceName: "four"}},
			expectedError:   "calculation error",
		},
	}

	for _, tc := range testCases {
		client := testclient.NewSimpleFake(tc.startingQuota())

		quotaUsageCalculationFunc = tc.calculationFunc
		// we only need these fields to test the sync func
		controller := ClusterQuotaReconcilationController{
			clusterQuotaMapper: tc.mapperFunc(),
			clusterQuotaClient: client,
		}

		actualErr, actualRetries := controller.syncQuotaForNamespaces(tc.startingQuota(), tc.workItems)
		switch {
		case len(tc.expectedError) == 0 && actualErr == nil:
		case len(tc.expectedError) == 0 && actualErr != nil:
			t.Errorf("%s: unexpected error: %v", tc.name, actualErr)
			continue
		case len(tc.expectedError) != 0 && actualErr == nil:
			t.Errorf("%s: missing expected error: %v", tc.name, tc.expectedError)
			continue
		case len(tc.expectedError) != 0 && actualErr != nil && !strings.Contains(actualErr.Error(), tc.expectedError):
			t.Errorf("%s: expected %v, got %v", tc.name, tc.expectedError, actualErr)
			continue
		}

		if !reflect.DeepEqual(actualRetries, tc.expectedRetries) {
			t.Errorf("%s: expected %v, got %v", tc.name, tc.expectedRetries, actualRetries)
			continue
		}

		var actualQuota *quotaapi.ClusterResourceQuota
		for _, action := range client.Actions() {
			updateAction, ok := action.(ktestclient.UpdateAction)
			if !ok {
				continue
			}
			if updateAction.Matches("update", "clusterresourcequotas") {
				actualQuota = updateAction.GetObject().(*quotaapi.ClusterResourceQuota)
				break
			}
		}

		if tc.expectedQuota() == nil && actualQuota == nil {
			continue
		}

		if tc.expectedQuota() == nil && actualQuota != nil {
			t.Errorf("%s: expected %v, got %v", tc.name, "nil", actualQuota)
			continue
		}

		// the internal representation doesn't have json tags and I want a better diff, so converting
		expectedV1, err := kapi.Scheme.ConvertToVersion(tc.expectedQuota(), quotaapiv1.SchemeGroupVersion.String())
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
			continue
		}
		actualV1, err := kapi.Scheme.ConvertToVersion(actualQuota, quotaapiv1.SchemeGroupVersion.String())
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
			continue
		}
		if !kapi.Semantic.DeepEqual(expectedV1, actualV1) {
			t.Errorf("%s: %v", tc.name, utildiff.ObjectDiff(expectedV1, actualV1))
			continue
		}
	}

}

type fakeClusterQuotaMapper struct {
	quotaToSelector   map[string]*unversioned.LabelSelector
	namespaceToLabels map[string]map[string]string

	quotaToNamespaces map[string]sets.String
	namespaceToQuota  map[string]sets.String
}

func newFakeClusterQuotaMapper() *fakeClusterQuotaMapper {
	return &fakeClusterQuotaMapper{
		quotaToSelector:   map[string]*unversioned.LabelSelector{},
		namespaceToLabels: map[string]map[string]string{},
		quotaToNamespaces: map[string]sets.String{},
		namespaceToQuota:  map[string]sets.String{},
	}
}

func (m *fakeClusterQuotaMapper) GetClusterQuotasFor(namespaceName string) ([]string, map[string]string) {
	return m.namespaceToQuota[namespaceName].List(), m.namespaceToLabels[namespaceName]
}
func (m *fakeClusterQuotaMapper) GetNamespacesFor(quotaName string) ([]string, *unversioned.LabelSelector) {
	return m.quotaToNamespaces[quotaName].List(), m.quotaToSelector[quotaName]
}
func (m *fakeClusterQuotaMapper) AddListener(listener clusterquotamapping.MappingChangeListener) {}

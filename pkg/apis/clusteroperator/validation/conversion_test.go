package validation

import (
	"testing"

	"github.com/stretchr/testify/assert"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openshift/cluster-operator/pkg/api"
	internal "github.com/openshift/cluster-operator/pkg/apis/clusteroperator"
	v1alpha1 "github.com/openshift/cluster-operator/pkg/apis/clusteroperator/v1alpha1"
)

func TestConversion(t *testing.T) {
	v1alpah1Spec := &v1alpha1.ClusterProviderConfigSpec{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1alpha1.SchemeGroupVersion.String(),
			Kind:       "ClusterProviderConfigSpec",
		},
		ClusterSpec: v1alpha1.ClusterSpec{
			ClusterVersionRef: v1alpha1.ClusterVersionReference{
				Namespace: "test-namespace",
				Name:      "test-name",
			},
		},
	}
	internalSpecObj, err := api.Scheme.ConvertToVersion(v1alpah1Spec, internal.SchemeGroupVersion)
	if !assert.NoError(t, err, "unexpected error during conversion") {
		return
	}
	if !assert.NotNil(t, internalSpecObj, "expected to get an internal spec") {
		return
	}
	internalSpec, ok := internalSpecObj.(*internal.ClusterProviderConfigSpec)
	if !assert.True(t, ok, "expected converted object to be a ClusterProviderConfigSpec") {
		return
	}
	//	if !assert.Equal(t, internal.SchemeGroupVersion.String(), internalSpec.APIVersion, "unexpected API version for converted object") {
	//		return
	//	}
	if !assert.Equal(t, "test-namespace", internalSpec.ClusterVersionRef.Namespace, "unexpected namespace") {
		return
	}
	if !assert.Equal(t, "test-name", internalSpec.ClusterVersionRef.Name, "unexpected name") {
		return
	}
}

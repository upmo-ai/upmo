package kubernetes

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func schemeParamCodec() runtime.ParameterCodec {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	return runtime.NewParameterCodec(s)
}

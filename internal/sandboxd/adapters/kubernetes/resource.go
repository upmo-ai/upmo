package kubernetes

import (
	"strconv"

	"k8s.io/apimachinery/pkg/api/resource"
)

func resourceQuantity(v string) resource.Quantity {
	return resource.MustParse(v)
}

func cpuQuantity(cores int) resource.Quantity {
	return resource.MustParse(strconv.Itoa(cores))
}

func memoryQuantity(bytes int64) resource.Quantity {
	return *resource.NewQuantity(bytes, resource.BinarySI)
}

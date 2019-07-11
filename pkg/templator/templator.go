package templator

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Templator is the interface for the templating engine.
type Templator interface {
	Template() ([]*unstructured.Unstructured, error)
}

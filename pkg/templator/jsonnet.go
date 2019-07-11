package templator

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	goyaml "github.com/ghodss/yaml"
	jsonnet "github.com/google/go-jsonnet"
	"github.com/google/go-jsonnet/ast"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
)

var (
	errorMainNotFound = errors.New("couldn't find either main.jsonnet or main.libsonnet")
)

// JsonnetTemplator is a jsonnet based templator.
type JsonnetTemplator struct {
	vm *jsonnet.VM

	file string
}

// NewJsonnetTemplator returns a new Templator backed by jsonnet.
func NewJsonnetTemplator(path string) (*JsonnetTemplator, error) {
	jpaths, file, err := getJPathsAndFile(path)
	if err != nil {
		return nil, err
	}

	vm := jsonnet.MakeVM()
	importer := jsonnet.FileImporter{
		JPaths: jpaths,
	}

	vm.Importer(&importer)
	RegisterNativeFuncs(vm)

	return &JsonnetTemplator{
		vm:   vm,
		file: file,
	}, nil
}

// Template implements Templator.
func (jt *JsonnetTemplator) Template() ([]*unstructured.Unstructured, error) {
	jsonnetBytes, err := ioutil.ReadFile(jt.file)
	if err != nil {
		return nil, err
	}

	jsonstr, err := jt.vm.EvaluateSnippet(jt.file, string(jsonnetBytes))
	if err != nil {
		return nil, err
	}

	return jsonTok8sObjs(jsonstr)
}

func getJPathsAndFile(path string) ([]string, string, error) {
	pwd, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}

	// Add 3 things:
	// 1. The .metadata file
	// 2. The lib dir
	// 3. The vendor dir

	// Finally the file is environments/<path>/main.libsonnet/jsonnet
	japths := append([]string{},
		filepath.Join(pwd, "lib"),
		filepath.Join(pwd, "vendor"),
		filepath.Join(path, ".metadata"),
	)

	path = filepath.Join("environments", path)
	file := filepath.Join(path, "main.libsonnet")
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		return japths, file, nil
	}

	file = filepath.Join(path, "main.jsonnet")
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		return japths, file, nil
	}

	return nil, "", errorMainNotFound
}

func jsonTok8sObjs(jsonstr string) ([]*unstructured.Unstructured, error) {
	var top interface{}
	if err := json.Unmarshal([]byte(jsonstr), &top); err != nil {
		return nil, err
	}

	objs, err := jsonWalk(top)
	if err != nil {
		return nil, err
	}

	ret := make([]runtime.Object, 0, len(objs))
	for _, v := range objs {
		// TODO: Going to json and back is a bit horrible
		data, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		obj, _, err := unstructured.UnstructuredJSONScheme.Decode(data, nil, nil)
		if err != nil {
			return nil, err
		}
		ret = append(ret, obj)
	}

	return FlattenToV1(ret), nil
}

// RegisterNativeFuncs adds kubecfg's native jsonnet functions to provided VM
func RegisterNativeFuncs(vm *jsonnet.VM) {
	// NB: libjsonnet native functions can only pass primitive
	// types, so some functions json-encode the arg.  These
	// "*FromJson" functions will be replaced by regular native
	// version when libjsonnet is able to support this.

	vm.NativeFunction(
		&jsonnet.NativeFunction{
			Name:   "parseJson",
			Params: ast.Identifiers{"json"},
			Func: func(dataString []interface{}) (res interface{}, err error) {
				data := []byte(dataString[0].(string))
				err = json.Unmarshal(data, &res)
				return
			},
		})

	vm.NativeFunction(
		&jsonnet.NativeFunction{
			Name:   "parseYaml",
			Params: ast.Identifiers{"yaml"},
			Func: func(dataString []interface{}) (interface{}, error) {
				data := []byte(dataString[0].(string))
				ret := []interface{}{}
				d := yaml.NewYAMLToJSONDecoder(bytes.NewReader(data))
				for {
					var doc interface{}
					if err := d.Decode(&doc); err != nil {
						if err == io.EOF {
							break
						}
						return nil, err
					}
					ret = append(ret, doc)
				}
				return ret, nil
			},
		})

	vm.NativeFunction(
		&jsonnet.NativeFunction{
			Name:   "manifestJsonFromJson",
			Params: ast.Identifiers{"json", "indent"},
			Func: func(data []interface{}) (interface{}, error) {
				indent := int(data[1].(float64))
				dataBytes := []byte(data[0].(string))
				dataBytes = bytes.TrimSpace(dataBytes)
				buf := bytes.Buffer{}
				if err := json.Indent(&buf, dataBytes, "", strings.Repeat(" ", indent)); err != nil {
					return "", err
				}
				buf.WriteString("\n")
				return buf.String(), nil
			},
		})

	vm.NativeFunction(
		&jsonnet.NativeFunction{
			Name:   "manifestYamlFromJson",
			Params: ast.Identifiers{"json"},
			Func: func(data []interface{}) (interface{}, error) {
				var input interface{}
				dataBytes := []byte(data[0].(string))
				if err := json.Unmarshal(dataBytes, &input); err != nil {
					return "", err
				}
				output, err := goyaml.Marshal(input)
				return string(output), err
			},
		})

	vm.NativeFunction(
		&jsonnet.NativeFunction{
			Name:   "escapeStringRegex",
			Params: ast.Identifiers{"str"},
			Func: func(s []interface{}) (interface{}, error) {
				return regexp.QuoteMeta(s[0].(string)), nil
			},
		})

	vm.NativeFunction(
		&jsonnet.NativeFunction{
			Name:   "regexMatch",
			Params: ast.Identifiers{"regex", "string"},
			Func: func(s []interface{}) (interface{}, error) {
				return regexp.MatchString(s[0].(string), s[1].(string))
			},
		})

	vm.NativeFunction(
		&jsonnet.NativeFunction{
			Name:   "regexSubst",
			Params: ast.Identifiers{"regex", "src", "repl"},
			Func: func(data []interface{}) (interface{}, error) {
				regex, src, repl := data[0].(string), data[1].(string), data[2].(string)

				r, err := regexp.Compile(regex)
				if err != nil {
					return "", err
				}
				return r.ReplaceAllString(src, repl), nil
			},
		})
}

// FlattenToV1 expands any List-type objects into their members, and
// cooerces everything to v1.Unstructured.  Panics if coercion
// encounters an unexpected object type.
func FlattenToV1(objs []runtime.Object) []*unstructured.Unstructured {
	ret := make([]*unstructured.Unstructured, 0, len(objs))
	for _, obj := range objs {
		switch o := obj.(type) {
		case *unstructured.UnstructuredList:
			for _, item := range o.Items {
				ret = append(ret, &item)
			}
		case *unstructured.Unstructured:
			ret = append(ret, o)
		default:
			panic("Unexpected unstructured object type")
		}
	}
	return ret
}

func jsonWalk(obj interface{}) ([]interface{}, error) {
	switch o := obj.(type) {
	case map[string]interface{}:
		if o["kind"] != nil && o["apiVersion"] != nil {
			return []interface{}{o}, nil
		}
		ret := []interface{}{}
		for _, v := range o {
			children, err := jsonWalk(v)
			if err != nil {
				return nil, err
			}
			ret = append(ret, children...)
		}
		return ret, nil
	case []interface{}:
		ret := make([]interface{}, 0, len(o))
		for _, v := range o {
			children, err := jsonWalk(v)
			if err != nil {
				return nil, err
			}
			ret = append(ret, children...)
		}
		return ret, nil
	default:
		return nil, fmt.Errorf("Unexpected object structure: %T", o)
	}
}

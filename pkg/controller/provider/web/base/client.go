package base

import (
	"encoding/json"
	"errors"
	"fmt"
	liberr "github.com/konveyor/controller/pkg/error"
	libmodel "github.com/konveyor/controller/pkg/inventory/model"
	"io/ioutil"
	"net/http"
	liburl "net/url"
	"reflect"
)

//
// Errors
var (
	ResourceNotResolvedErr = errors.New("resource (kind) not resolved")
)

//
// Resolves resources to API paths.
type Resolver interface {
	// Find the API path for the specified resource.
	Path(resource interface{}, id string) (string, error)
}

//
// REST API client.
type Client struct {
	Resolver
	// Bearer token.
	Token string
	// Host <host>:<port>
	Host string
	// Parameters
	Params Params
}

//
// Get a resource.
func (c *Client) Get(resource interface{}, id string) (status int, err error) {
	if c.Resolver == nil {
		err = liberr.Wrap(ResourceNotResolvedErr)
		return
	}
	lv := reflect.ValueOf(resource)
	switch lv.Kind() {
	case reflect.Ptr:
	default:
		return -1, libmodel.MustBePtrErr
	}
	path, err := c.Resolver.Path(resource, id)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	status, err = c.get(path, resource)

	return
}

//
// List resources in a collection.
func (c *Client) List(list interface{}) (status int, err error) {
	var resource interface{}
	lt := reflect.TypeOf(list)
	lv := reflect.ValueOf(list)
	switch lv.Kind() {
	case reflect.Ptr:
		lt := lt.Elem()
		lv = lv.Elem()
		switch lv.Kind() {
		case reflect.Slice:
			resource = reflect.New(lt.Elem()).Interface()
		default:
			return -1, libmodel.MustBeSlicePtrErr
		}
	default:
		return -1, libmodel.MustBeSlicePtrErr
	}
	path, err := c.Resolver.Path(resource, "")
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	status, err = c.get(path, resource)

	return
}

//
// Http GET
func (c *Client) get(path string, resource interface{}) (status int, err error) {
	header := http.Header{}
	if c.Token != "" {
		header["Authorization"] = []string{
			fmt.Sprintf("Bearer %s", c.Token),
		}
	}
	request := &http.Request{
		Method: http.MethodGet,
		Header: header,
		URL:    c.url(path),
	}
	client := http.Client{}
	response, err := client.Do(request)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	status = response.StatusCode
	content := []byte{}
	if status == http.StatusOK {
		defer response.Body.Close()
		content, err = ioutil.ReadAll(response.Body)
		if err != nil {
			err = liberr.Wrap(err)
			return
		}
		err = json.Unmarshal(content, resource)
		if err != nil {
			err = liberr.Wrap(err)
			return
		}
	}

	return
}

//
// Build the URL.
func (c *Client) url(path string) *liburl.URL {
	if c.Host == "" {
		c.Host = "localhost:8080"
	}
	path = (&Handler{}).Link(path, c.Params)
	url, _ := liburl.Parse(path)
	if url.Host == "" {
		url.Scheme = "http"
		url.Host = c.Host
	}

	return url
}
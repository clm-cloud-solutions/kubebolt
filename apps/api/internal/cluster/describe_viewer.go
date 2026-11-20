package cluster

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/kubectl/pkg/describe"
)

// kubectl's describers read the object themselves and print it: a ConfigMap's
// every value, a generic CRD's whole spec (an Argo CD Application's Helm
// values). These describers read through a client that hides what the YAML
// viewer hides before kubectl sees the object, so describe and the viewer
// cannot disagree.

// DescriberFor is describe.DescriberFor over the viewer's client.
func DescriberFor(gk schema.GroupKind, cfg *rest.Config) (describe.ResourceDescriber, bool) {
	return describe.DescriberFor(gk, viewerConfig(cfg))
}

// GenericDescriberFor is describe.GenericDescriberFor over the viewer's client.
func GenericDescriberFor(mapping *meta.RESTMapping, cfg *rest.Config) (describe.ResourceDescriber, bool) {
	return describe.GenericDescriberFor(mapping, viewerConfig(cfg))
}

// viewerConfig is cfg asking for JSON (protobuf could not be inspected) through
// a transport that hides, in every object a GET returns, what the viewer hides.
func viewerConfig(cfg *rest.Config) *rest.Config {
	c := rest.CopyConfig(cfg)
	c.ContentType = "application/json"
	c.AcceptContentTypes = "application/json"
	c.Wrap(func(rt http.RoundTripper) http.RoundTripper { return viewerTransport{rt: rt} })
	return c
}

type viewerTransport struct{ rt http.RoundTripper }

func (t viewerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.rt.RoundTrip(req)
	if err != nil || req.Method != http.MethodGet || resp.StatusCode != http.StatusOK || resp.Body == nil {
		return resp, err
	}
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if resp.Header.Get("Content-Encoding") == "gzip" {
		zr, zerr := gzip.NewReader(bytes.NewReader(raw))
		if zerr != nil {
			return nil, zerr
		}
		raw, err = io.ReadAll(zr)
		if err != nil {
			return nil, err
		}
		resp.Header.Del("Content-Encoding")
	}
	body := hideInResponse(raw)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Del("Content-Length")
	return resp, nil
}

// hideInResponse applies the viewer's rule to one object. A Secret is left as
// it is: its describer prints only each value's size, which the placeholder
// would falsify.
func hideInResponse(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var obj map[string]interface{}
	if err := dec.Decode(&obj); err != nil {
		return body
	}
	if group, kind := objectGroupKind(obj); group == "" && kind == "Secret" {
		return body
	}
	if !hideFromViewer(obj) {
		return body
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

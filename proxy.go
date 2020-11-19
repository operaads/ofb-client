package api_client

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"github.com/operaads/api-client/proxy"
	"github.com/operaads/api-client/request"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type ProxyRequestType string

const (
	ProxyRequestTypeNone          = ProxyRequestType("")
	ProxyRequestTypeRaw           = ProxyRequestType("RAW")
	ProxyRequestTypeForm          = ProxyRequestType("FORM")
	ProxyRequestTypeMultipartForm = ProxyRequestType("MULTIPART_FORM")
)

func (c *Client) ProxyAPI(
	method, path string,
	httpReq *http.Request,
	writer http.ResponseWriter,
	requestType ProxyRequestType,
	opts ...proxy.Option,
) error {
	if path == "" {
		u := &url.URL{
			Path:     httpReq.URL.Path,
			RawQuery: httpReq.URL.RawQuery,
			Fragment: httpReq.URL.Fragment,
		}
		path = u.String()
	}

	// if method is empty, set to http's request method
	if method == "" {
		method = httpReq.Method
	}

	opt := &proxy.Options{
		RequestTimeout: c.Timeout,
	}

	for _, o := range opts {
		o(opt)
	}

	var reqParseFunc func(*http.Request, *proxy.Options) (io.Reader, string, error)

	switch requestType {
	case ProxyRequestTypeRaw:
		reqParseFunc = parseRawRequest
	case ProxyRequestTypeForm:
		reqParseFunc = parseFormRequest
	case ProxyRequestTypeMultipartForm:
		reqParseFunc = parseMultipartFormRequest
	default:
		reqParseFunc = func(req *http.Request, opt *proxy.Options) (io.Reader, string, error) {
			return nil, "", nil
		}
	}

	reqBody, reqContentType, err := reqParseFunc(httpReq, opt)
	if err != nil {
		return err
	}

	requestOptions := []request.Option{
		request.WithRequestInterceptors(func(r *http.Request) {
			for k, vv := range httpReq.Header {
				for _, v := range vv {
					r.Header.Add(k, v)
				}
			}

			if reqContentType != "" {
				r.Header.Set("Content-Type", reqContentType)
			}
		}),
		request.WithRequestTimeout(opt.RequestTimeout),
	}

	if opt.URLInterceptor != nil {
		requestOptions = append(
			requestOptions,
			request.AppendURLInterceptors(opt.URLInterceptor),
		)
	}
	if opt.RequestInterceptor != nil {
		requestOptions = append(
			requestOptions,
			request.AppendRequestInterceptors(opt.RequestInterceptor),
		)
	}

	apiReq := request.NewAPIRequest(
		method, path, reqBody,
		requestOptions...,
	)

	res, err := c.DoAPIRequest(apiReq)
	if err != nil {
		return err
	}

	defer res.Body.Close()

	// transfer response headers
	for k, vv := range res.Header {
		for _, h := range opt.TransferResponseHeaders {
			if k == h {
				for _, v := range vv {
					writer.Header().Add(k, v)
				}
				break
			}
		}
	}

	// write status code
	writer.WriteHeader(res.StatusCode)

	resContentEncoding := res.Header.Get("Content-Encoding")

	if opt.ResponseJSONInterceptor != nil {
		var m interface{}

		var reader io.Reader
		switch resContentEncoding {
		case "gzip":
			if gzReader, err := gzip.NewReader(res.Body); err != nil {
				return err
			} else {
				reader = gzReader
			}
		default:
			reader = res.Body
		}

		if err := json.NewDecoder(reader).Decode(&m); err != nil {
			return err
		}

		m = opt.ResponseJSONInterceptor(m)

		buf := new(bytes.Buffer)
		if err := json.NewEncoder(buf).Encode(m); err != nil {
			return err
		}

		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.Header().Set("Content-Length", strconv.Itoa(buf.Len()))

		if _, err := writer.Write(buf.Bytes()); err != nil {
			return err
		}

		return nil
	}

	writer.Header().Set("Content-Type", res.Header.Get("Content-Type"))

	if resContentLength := res.Header.Get("Content-Length"); resContentLength != "" {
		writer.Header().Set("Content-Length", resContentLength)
	}

	if resContentEncoding != "" {
		writer.Header().Set("Content-Encoding", resContentEncoding)
	}

	// copy response
	_, err = io.Copy(writer, res.Body)
	return err
}

func (c *Client) ProxyJSONAPI(
	method, path string,
	httpReq *http.Request,
	writer http.ResponseWriter,
	opts ...proxy.Option,
) error {
	return c.ProxyAPI(method, path, httpReq, writer, ProxyRequestTypeRaw, opts...)
}

func (c *Client) TransparentProxyJSONAPI(httpReq *http.Request, writer http.ResponseWriter) error {
	return c.ProxyJSONAPI("", "", httpReq, writer)
}

func (c *Client) ProxyFormAPI(
	method, path string,
	httpReq *http.Request,
	writer http.ResponseWriter,
	opts ...proxy.Option,
) error {
	return c.ProxyAPI(method, path, httpReq, writer, ProxyRequestTypeForm, opts...)
}

func (c *Client) TransparentProxyFormAPI(httpReq *http.Request, writer http.ResponseWriter) error {
	return c.ProxyAPI("", "", httpReq, writer, ProxyRequestTypeForm)
}

func (c *Client) ProxyMultipartFormAPI(
	method, path string,
	httpReq *http.Request,
	writer http.ResponseWriter,
	opts ...proxy.Option,
) error {
	return c.ProxyAPI(method, path, httpReq, writer, ProxyRequestTypeMultipartForm, opts...)
}

func (c *Client) TransparentProxyMultipartFormAPI(httpReq *http.Request, writer http.ResponseWriter) error {
	return c.ProxyAPI("", "", httpReq, writer, ProxyRequestTypeMultipartForm)
}

func parseRawRequest(req *http.Request, opt *proxy.Options) (io.Reader, string, error) {
	if opt.RequestJSONInterceptor != nil {
		defer req.Body.Close()

		var m interface{}

		if err := json.NewDecoder(req.Body).Decode(&m); err != nil {
			return nil, "", err
		}

		m = opt.RequestJSONInterceptor(m)

		buf := new(bytes.Buffer)
		if err := json.NewEncoder(buf).Encode(m); err != nil {
			return nil, "", err
		}

		return buf, "application/json; charset=utf-8", nil
	}

	contentType := req.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	return req.Body, contentType, nil
}

func parseFormRequest(req *http.Request, opt *proxy.Options) (io.Reader, string, error) {
	if err := req.ParseForm(); err != nil {
		return nil, "", err
	}

	form := url.Values{}
	for k, vv := range req.PostForm {
		for _, v := range vv {
			form.Add(k, v)
		}
	}

	if opt.RequestFormInterceptor != nil {
		form = opt.RequestFormInterceptor(form)
	}

	contentType := req.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/x-www-form-urlencoded"
	}

	return strings.NewReader(form.Encode()), contentType, nil
}

func parseMultipartFormRequest(req *http.Request, opt *proxy.Options) (io.Reader, string, error) {
	if err := req.ParseMultipartForm(opt.MaxUploadSize); err != nil {
		return nil, "", err
	}

	reqBody := new(bytes.Buffer)
	multiWriter := multipart.NewWriter(reqBody)

	defer multiWriter.Close()

	for k, vv := range req.MultipartForm.Value {
		for _, v := range vv {
			if err := multiWriter.WriteField(k, v); err != nil {
				return nil, "", err
			}
		}
	}

	for k, vv := range req.MultipartForm.File {
		for _, v := range vv {
			f, err := v.Open()
			if err != nil {
				return nil, "", err
			}
			writer, err := multiWriter.CreateFormFile(k, v.Filename)
			if err != nil {
				return nil, "", err
			}
			if _, err := io.Copy(writer, f); err != nil {
				return nil, "", err
			}

			f.Close()
		}
	}

	if opt.RequestMultipartFormInterceptor != nil {
		opt.RequestMultipartFormInterceptor(multiWriter)
	}

	return reqBody, multiWriter.FormDataContentType(), nil
}

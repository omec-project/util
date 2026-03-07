// Copyright 2019 Communication Service/Software Laboratory, National Chiao Tung University (free5gc.org)
//
// SPDX-License-Identifier: Apache-2.0

package httpwrapper

import (
	"net/http"
	"net/url"
)

type Request struct {
	Params map[string]string
	Header http.Header
	Query  url.Values
	Body   any
	URL    *url.URL
}

func NewRequest(req *http.Request, body any) *Request {
	ret := &Request{}
	ret.Query = req.URL.Query()
	ret.Header = req.Header
	ret.Body = body
	ret.Params = make(map[string]string)
	ret.URL = req.URL
	return ret
}

type Response struct {
	Header http.Header
	Status int
	Body   any
}

func NewResponse(code int, h http.Header, body any) *Response {
	ret := &Response{}
	ret.Status = code
	ret.Header = h
	ret.Body = body
	return ret
}

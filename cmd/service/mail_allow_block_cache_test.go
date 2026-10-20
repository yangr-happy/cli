// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/larksuite/cli/internal/client"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

func TestUserAllowBlockSearchCacheRecoveryEmptyList(t *testing.T) {
	request := allowBlockSearchRequest()
	var calls []map[string]interface{}
	var sleeps []time.Duration
	responses := []*larkcore.ApiResp{
		serviceTestResponse(map[string]interface{}{"code": 456, "msg": "ErrCacheEmpty"}),
		serviceTestResponse(map[string]interface{}{"code": 0, "data": map[string]interface{}{"items": []interface{}{}}}),
	}

	got, err := runUserAllowBlockSearchWithCacheRecovery(context.Background(), "user_mailbox.allow_sender.list", request,
		sequenceServiceCaller(t, responses, &calls), func(delay time.Duration) { sleeps = append(sleeps, delay) })
	if err != nil {
		t.Fatalf("runUserAllowBlockSearchWithCacheRecovery() error = %v", err)
	}
	assertServiceResponseItems(t, got, 0)
	if len(calls) != 2 || calls[0]["keyword"] != "friend@example.com" {
		t.Fatalf("calls = %#v, want keyword search then unfiltered list", calls)
	}
	if _, exists := calls[1]["keyword"]; exists {
		t.Fatalf("list call params = %#v, want no keyword", calls[1])
	}
	if len(sleeps) != 0 {
		t.Fatalf("sleeps = %v, want none for empty list", sleeps)
	}
}

func TestUserAllowBlockSearchCacheRecoveryRetrySuccess(t *testing.T) {
	request := allowBlockSearchRequest()
	var calls []map[string]interface{}
	var sleeps []time.Duration
	responses := []*larkcore.ApiResp{
		serviceTestResponse(map[string]interface{}{"code": 456, "msg": "456:0:ErrCacheEmpty:0::::[]"}),
		serviceTestResponse(map[string]interface{}{"code": 0, "data": map[string]interface{}{"items": []interface{}{map[string]interface{}{"sender": "other@example.com"}}}}),
		serviceTestResponse(map[string]interface{}{"code": 0, "data": map[string]interface{}{"items": []interface{}{map[string]interface{}{"sender": "friend@example.com"}}}}),
	}

	got, err := runUserAllowBlockSearchWithCacheRecovery(context.Background(), "user_mailbox.blocked_sender.list", request,
		sequenceServiceCaller(t, responses, &calls), func(delay time.Duration) { sleeps = append(sleeps, delay) })
	if err != nil {
		t.Fatalf("runUserAllowBlockSearchWithCacheRecovery() error = %v", err)
	}
	assertServiceResponseItems(t, got, 1)
	if len(calls) != 3 {
		t.Fatalf("calls = %#v, want search, list, one search retry", calls)
	}
	if calls[0]["keyword"] != "friend@example.com" || calls[2]["keyword"] != "friend@example.com" {
		t.Fatalf("calls = %#v, want original keyword on initial and retry requests", calls)
	}
	if _, exists := calls[1]["keyword"]; exists {
		t.Fatalf("list call params = %#v, want no keyword", calls[1])
	}
	if !reflect.DeepEqual(sleeps, []time.Duration{2 * time.Second}) {
		t.Fatalf("sleeps = %v, want [2s]", sleeps)
	}
}

func TestUserAllowBlockSearchCacheRecoveryPersistentMissReturnsEmpty(t *testing.T) {
	request := allowBlockSearchRequest()
	var calls []map[string]interface{}
	var sleeps []time.Duration
	responses := []*larkcore.ApiResp{
		serviceTestResponse(map[string]interface{}{"code": 456, "msg": "ErrCacheEmpty"}),
		serviceTestResponse(map[string]interface{}{"code": 0, "data": map[string]interface{}{
			"items": []interface{}{map[string]interface{}{"sender": "other@example.com"}}, "has_more": true, "page_token": "next",
		}}),
		serviceTestResponse(map[string]interface{}{"code": 456, "message": "ErrCacheEmpty"}),
	}

	got, err := runUserAllowBlockSearchWithCacheRecovery(context.Background(), "user_mailbox.allow_sender.list", request,
		sequenceServiceCaller(t, responses, &calls), func(delay time.Duration) { sleeps = append(sleeps, delay) })
	if err != nil {
		t.Fatalf("runUserAllowBlockSearchWithCacheRecovery() error = %v", err)
	}
	result := assertServiceResponseItems(t, got, 0)
	if !serviceNumberEquals(result["code"], 0) {
		t.Fatalf("code = %#v, want success", result["code"])
	}
	data, _ := result["data"].(map[string]interface{})
	if data["has_more"] != false || data["page_token"] != nil {
		t.Fatalf("data = %#v, want terminal empty search result", data)
	}
	if len(calls) != 3 || !reflect.DeepEqual(sleeps, []time.Duration{2 * time.Second}) {
		t.Fatalf("calls/sleeps = %d/%v, want 3/[2s]", len(calls), sleeps)
	}
}

func TestUserAllowBlockSearchCacheRecoveryPreservesOtherErrors(t *testing.T) {
	transportErr := errors.New("transport failed")
	tests := []struct {
		name          string
		responses     []*larkcore.ApiResp
		callErrAt     int
		wantCode      int
		wantCalls     int
		wantTransport bool
		wantSleep     bool
	}{
		{
			name: "initial non cache error", responses: []*larkcore.ApiResp{
				serviceTestResponse(map[string]interface{}{"code": 500, "msg": "ErrCacheEmpty"}),
			}, wantCode: 500, wantCalls: 1,
		},
		{
			name: "unfiltered list business error", responses: []*larkcore.ApiResp{
				serviceTestResponse(map[string]interface{}{"code": 456, "msg": "ErrCacheEmpty"}),
				serviceTestResponse(map[string]interface{}{"code": 403, "msg": "permission denied"}),
			}, wantCode: 403, wantCalls: 2,
		},
		{
			name: "retry non cache error", responses: []*larkcore.ApiResp{
				serviceTestResponse(map[string]interface{}{"code": 456, "msg": "ErrCacheEmpty"}),
				serviceTestResponse(map[string]interface{}{"code": 0, "data": map[string]interface{}{"items": []interface{}{"present"}}}),
				serviceTestResponse(map[string]interface{}{"code": 429, "msg": "rate limited"}),
			}, wantCode: 429, wantCalls: 3, wantSleep: true,
		},
		{
			name: "unfiltered list transport error", responses: []*larkcore.ApiResp{
				serviceTestResponse(map[string]interface{}{"code": 456, "msg": "ErrCacheEmpty"}),
			}, callErrAt: 2, wantCalls: 2, wantTransport: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls []map[string]interface{}
			var sleeps []time.Duration
			responseIndex := 0
			got, err := runUserAllowBlockSearchWithCacheRecovery(context.Background(), "user_mailbox.allow_sender.list", allowBlockSearchRequest(),
				func(_ context.Context, request client.RawApiRequest) (*larkcore.ApiResp, error) {
					calls = append(calls, cloneParams(request.Params))
					if tc.callErrAt == len(calls) {
						return nil, transportErr
					}
					response := tc.responses[responseIndex]
					responseIndex++
					return response, nil
				}, func(delay time.Duration) { sleeps = append(sleeps, delay) })
			if tc.wantTransport {
				if !errors.Is(err, transportErr) {
					t.Fatalf("error = %v, want transport error", err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected transport error: %v", err)
				}
				result, ok := serviceJSONResult(got)
				if !ok || !serviceNumberEquals(result["code"], tc.wantCode) {
					t.Fatalf("result = %#v, want code %d", result, tc.wantCode)
				}
			}
			if len(calls) != tc.wantCalls {
				t.Fatalf("calls = %d, want %d", len(calls), tc.wantCalls)
			}
			if (len(sleeps) > 0) != tc.wantSleep {
				t.Fatalf("sleeps = %v, wantSleep=%v", sleeps, tc.wantSleep)
			}
		})
	}
}

func allowBlockSearchRequest() client.RawApiRequest {
	return client.RawApiRequest{
		Method: "GET",
		URL:    "/open-apis/mail/v1/user_mailboxes/me/allow_senders",
		Params: map[string]interface{}{"user_mailbox_id": "me", "keyword": "friend@example.com", "page_size": 10},
	}
}

func sequenceServiceCaller(t *testing.T, responses []*larkcore.ApiResp, calls *[]map[string]interface{}) serviceRequestFunc {
	t.Helper()
	index := 0
	return func(_ context.Context, request client.RawApiRequest) (*larkcore.ApiResp, error) {
		*calls = append(*calls, cloneParams(request.Params))
		if index >= len(responses) {
			t.Fatalf("unexpected request %d: %#v", index+1, request.Params)
		}
		response := responses[index]
		index++
		return response, nil
	}
}

func cloneParams(params map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(params))
	for key, value := range params {
		cloned[key] = value
	}
	return cloned
}

func serviceTestResponse(body map[string]interface{}) *larkcore.ApiResp {
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return &larkcore.ApiResp{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		RawBody:    raw,
	}
}

func assertServiceResponseItems(t *testing.T, response *larkcore.ApiResp, want int) map[string]interface{} {
	t.Helper()
	result, ok := serviceJSONResult(response)
	if !ok {
		t.Fatalf("response is not JSON: %#v", response)
	}
	data, _ := result["data"].(map[string]interface{})
	items, _ := data["items"].([]interface{})
	if len(items) != want {
		t.Fatalf("items = %#v, want length %d", items, want)
	}
	return result
}

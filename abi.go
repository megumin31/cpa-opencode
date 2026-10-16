package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"unsafe"
)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil || host == nil || host.abi_version != 1 {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = 1
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) (rc C.int) {
	if response == nil {
		return 1
	}
	response.ptr = nil
	response.len = 0
	defer func() {
		if recover() != nil {
			writeResponse(response, errorEnvelope(fmt.Errorf("internal plugin failure")))
			rc = 1
		}
	}()
	if method == nil || requestLen > 64*1024*1024 || (requestLen > 0 && request == nil) {
		writeResponse(response, errorEnvelope(fail(400, "invalid RPC request")))
		return 1
	}
	var raw []byte
	if requestLen > 0 {
		raw = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	result, err := handleMethod(C.GoString(method), raw)
	if err != nil {
		writeResponse(response, errorEnvelope(err))
		return 1
	}
	body, err := json.Marshal(result)
	if err != nil {
		writeResponse(response, errorEnvelope(err))
		return 1
	}
	out, _ := json.Marshal(pluginabi.Envelope{OK: true, Result: body})
	writeResponse(response, out)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() { shutdown(); C.store_host_api(nil) }

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	response.ptr = C.CBytes(raw)
	response.len = C.size_t(len(raw))
}
func errorEnvelope(err error) []byte {
	status := 500
	var e *statusError
	if errors.As(err, &e) {
		status = e.status
	}
	out, _ := pluginabi.NewErrorEnvelope("opencode_error", err.Error(), status)
	return out
}
func callHost(method string, payload any) (json.RawMessage, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	cm := C.CString(method)
	defer C.free(unsafe.Pointer(cm))
	p := C.CBytes(raw)
	defer C.free(p)
	var response C.cliproxy_buffer
	code := C.call_host_api(cm, (*C.uint8_t)(p), C.size_t(len(raw)), &response)
	if response.ptr == nil {
		return nil, fmt.Errorf("host callback unavailable")
	}
	defer C.free_host_buffer(response.ptr, response.len)
	if response.len > 64*1024*1024 {
		return nil, fmt.Errorf("host response too large")
	}
	var env pluginabi.Envelope
	if err := json.Unmarshal(C.GoBytes(response.ptr, C.int(response.len)), &env); err != nil {
		return nil, fmt.Errorf("invalid host callback response")
	}
	if !env.OK || code != 0 {
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	return env.Result, nil
}

/*
 * Copyright NetFoundry, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * https://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 */

package cziti

/*
#cgo windows LDFLAGS: -l libziti.imp -luv -lws2_32 -lpsapi

#include <ziti/ziti.h>

#include "sdk.h"
extern void initCB(ziti_context nf, int status, void *ctx);
extern void serviceCB(ziti_context nf, ziti_service*, int status, void *ctx);
extern void cron_callback(uv_async_t *handle);
extern void shutdown_callback(uv_async_t *handle);
extern void free_async(uv_handle_t* timer);

*/
import "C"
import (
	"encoding/json"
	"errors"
	"github.com/michaelquigley/pfxlog"
	"github.com/openziti/desktop-edge-win/service/ziti-tunnel/config"
	"github.com/robfig/cron/v3"
	"io/ioutil"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"
)

const (
	ADDED = "added"
	REMOVED = "removed"
)

var ServiceChanges = make(chan ServiceChange, 256)
var log = pfxlog.Logger()
var c = cron.New()

type sdk struct {
	libuvCtx *C.libuv_ctx
}
type ServiceChange struct {
	Operation   string
	Service		*Service
	ZitiContext   *CZitiCtx
}

var _impl sdk

func init() {
	_impl.libuvCtx = (*C.libuv_ctx)(C.calloc(1, C.sizeof_libuv_ctx))
	C.libuv_init(_impl.libuvCtx)
}

func SetLog(f *os.File) {
	C.setLogOut(C.intptr_t(f.Fd()))
}

func SetLogLevel(level int) {
	log.Infof("Setting cziti log level to: %d", level)
	C.setLogLevel(C.int(level))
}

func Start() {
	v := C.ziti_get_version()
	log.Infof("starting ziti-sdk-c %s(%s)[%s]", C.GoString(v.version), C.GoString(v.revision), C.GoString(v.build_date))

	_impl.run()
}

func (inst *sdk) run() {
	C.libuv_run(inst.libuvCtx)
}

func Stop() {
	C.libuv_stop(_impl.libuvCtx)
}

type Service struct {
	Name          string
	Id            string
	InterceptHost string
	InterceptPort uint16
	AssignedIP    string
	OwnsIntercept bool
}

type CZitiCtx struct {
	options   C.ziti_options
	zctx      C.ziti_context
	status    int
	statusErr error

	Services *sync.Map
}

func (c *CZitiCtx) Status() (int, error) {
	return c.status, c.statusErr
}

func (c *CZitiCtx) Name() string {
	if c.zctx != nil {
		id := C.ziti_get_identity(c.zctx)
		if id != nil {
			return C.GoString(id.name)
		}
	}
	return "<unknown>"
}

func (c *CZitiCtx) Controller() string {
	if c.zctx != nil {
		return C.GoString(C.ziti_get_controller(c.zctx))
	}
	return C.GoString(c.options.controller)
}

var tunCfgName = C.CString("ziti-tunneler-client.v1")

//export serviceCB
func serviceCB(_ C.ziti_context, service *C.ziti_service, status C.int, data unsafe.Pointer) {
	ctx := (*CZitiCtx)(data)

	if ctx.Services == nil {
		m := sync.Map{} //make(map[string]Service)
		ctx.Services = &m
	}

	name := C.GoString(service.name)
	svcId := C.GoString(service.id)
	log.Debugf("============ INSIDE serviceCB - status: %s:%s - %v, %v, %v ============", name, svcId, status, C.ZITI_SERVICE_UNAVAILABLE, C.ZITI_OK)
	if status == C.ZITI_SERVICE_UNAVAILABLE {
		found, ok := ctx.Services.Load(svcId)
		fs := found.(Service)
		if ok {
			DNS.DeregisterService(ctx, name)
			ctx.Services.Delete(svcId)
			ServiceChanges <- ServiceChange{
				Operation: REMOVED,
				Service:   &fs,
				ZitiContext: ctx,
			}
		} else {
			log.Warnf("could not find a service with id: %s, name: %s", service.id, service.name)
		}
	} else if status == C.ZITI_OK {
		cfg := C.ziti_service_get_raw_config(service, tunCfgName)

		host := ""
		port := -1
		if cfg != nil {
			var c map[string]interface{}

			if err := json.Unmarshal([]byte(C.GoString(cfg)), &c); err == nil {
				host = c["hostname"].(string)
				port = int(c["port"].(float64))
			}
		}
		if host != "" && port != -1 {
			ownsIntercept := true
			ip, err := DNS.RegisterService(svcId, host, uint16(port), ctx, name)
			if err != nil {
				log.Warn(err)
				ownsIntercept = false
			} else {
				log.Infof("service intercept beginning for service: %s@%s:%d on ip %s", name, host, port, ip.String())
				for _, t := range devMap {
					t.AddIntercept(svcId, name, ip.String(), port, unsafe.Pointer(ctx.zctx))
				}
			}
			added := Service{
				Name:          name,
				Id:            svcId,
				InterceptHost: host,
				InterceptPort: uint16(port),
				AssignedIP:    ip.String(),
				OwnsIntercept: ownsIntercept,
			}
			ctx.Services.Store(svcId, added)
			ServiceChanges <- ServiceChange{
				Operation:   ADDED,
				Service: &added,
				ZitiContext: ctx,
			}
		} else {
			log.Debugf("service named %s is not enabled for 'tunneling'", name)
		}
	}
}

//export initCB
func initCB(nf C.ziti_context, status C.int, data unsafe.Pointer) {
	ctx := (*CZitiCtx)(data)

	ctx.zctx = nf
	ctx.options.ctx = data
	ctx.status = int(status)
	ctx.statusErr = zitiError(status)

	cfg := C.GoString(ctx.options.config)
	if ch, ok := initMap[cfg]; ok {
		ch <- ctx
	} else {
		log.Warn("response channel not found")
	}
}

var initMap = make(map[string]chan *CZitiCtx)

func zitiError(code C.int) error {
	if int(code) != 0 {
		return errors.New(C.GoString(C.ziti_errorstr(code)))
	}
	return nil
}

func LoadZiti(cfg string) *CZitiCtx {
	ctx := &CZitiCtx{}
	ctx.options.config = C.CString(cfg)
	ctx.options.init_cb = C.ziti_init_cb(C.initCB)
	ctx.options.service_cb = C.ziti_service_cb(C.serviceCB)
	ctx.options.refresh_interval = C.long(15)
	ctx.options.metrics_type = C.INSTANT
	ctx.options.config_types = C.all_configs

	ch := make(chan *CZitiCtx)
	initMap[cfg] = ch
	rc := C.ziti_init_opts(&ctx.options, _impl.libuvCtx.l, unsafe.Pointer(ctx))
	if rc != C.ZITI_OK {
		ctx.status, ctx.statusErr = int(rc), zitiError(rc)
		go func() {
			ch <- ctx
		}()
	}

	res := <-ch
	delete(initMap, cfg)

	return res
}

func GetTransferRates(ctx *CZitiCtx) (int64, int64, bool) { //extern void NF_get_transfer_rates(ziti_context nf, double* up, double* down);
	if ctx == nil {
		return 0, 0, false
	}
	var up, down C.double
	C.ziti_get_transfer_rates(ctx.zctx, &up, &down)

	return int64(up), int64(down), true
}

func(c *CZitiCtx) Shutdown() {
	if c != nil && c.zctx != nil {
		async := (*C.uv_async_t)(C.malloc(C.sizeof_uv_async_t))
		async.data = unsafe.Pointer(c.zctx)
		C.uv_async_init(_impl.libuvCtx.l, async, C.uv_async_cb(C.cron_callback))
		C.uv_async_send((*C.uv_async_t)(unsafe.Pointer(async)))
	} else {
		log.Info("shutdown called for identity but it has no context")
	}
}

var logFile *os.File //the current, active log file
var logLevel int //set in InitializeCLogger

func InitializeCLogger(level int) {
	logLevel = level
	SetLogLevel(logLevel)
	initializeLogForToday()

	c.AddFunc("@midnight", initiateRollLog)
	c.Start()
}

func initiateRollLog() {
	async := (*C.uv_async_t)(C.malloc(C.sizeof_uv_async_t))
	C.uv_async_init(_impl.libuvCtx.l, async, C.uv_async_cb(C.cron_callback))
	C.uv_async_send((*C.uv_async_t)(unsafe.Pointer(async)))
}

//export free_async
func free_async(handle *C.uv_handle_t){
	C.free(unsafe.Pointer(handle))
}

//export shutdown_callback
func shutdown_callback(async *C.uv_async_t) {
	log.Debug("shutting down c-sdk ziti context")
	C.ziti_shutdown((C.ziti_context)(async.data))
	C.uv_close((*C.uv_handle_t)(unsafe.Pointer(async)), C.uv_close_cb(C.free_async))
}

//export cron_callback
func cron_callback(async *C.uv_async_t) {
	// roll the log while on the uv loop
	if logFile == nil {
		log.Warn("log file is nil. this is unexpected. log rolling aborted")
	}

	_ = logFile.Close() //close the log file

	//rename the log file to 'now'
	nowFormatted := time.Now().Format("2006-01-02-030405")
	_ = os.Rename(config.Path()+"cziti.log", config.Path()+"cziti-" + nowFormatted + ".log")

	// set the log file in the c sdk
	initializeLogForToday()

	// find any logs older than 7 days and remove them
	removeFilesOlderThanRetentionPolicy()

	C.uv_close((*C.uv_handle_t)(unsafe.Pointer(async)), C.uv_close_cb(C.free_async))
}

func initializeLogForToday() {
	var err error
	logFile, err = os.OpenFile(config.Path()+"cziti.log", os.O_WRONLY|os.O_TRUNC|os.O_APPEND|os.O_CREATE, 0644)
	if err != nil {
		log.Warnf("could not open cziti.log for writing. no debug information will be captured.")
	} else {
		SetLog(logFile)
	}
}

func removeFilesOlderThanRetentionPolicy() {
	logFiles, err := ioutil.ReadDir(config.Path())
	if err != nil {
		return
	}
	now := time.Now()
	for _, file := range logFiles {
		if !strings.HasPrefix(file.Name(), "cziti-") {
			continue
		}
		if file.Mode().IsRegular() {
			if isOlderThanRetentionPolicy(now, file.ModTime()) {
				log.Infof("removing file %s because it is older than the retention policy.", file.Name())
				os.Remove(config.Path() + file.Name())
			}
		}
	}
	return
}

func isOlderThanRetentionPolicy(asOf time.Time, t time.Time) bool {
	oneWeek := 7 * 24 * time.Hour
	return asOf.Sub(t) > oneWeek
}

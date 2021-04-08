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

#include <stdlib.h>

#include <ziti/ziti.h>
#include <ziti/ziti_events.h>
#include <ziti/ziti_tunnel.h>
#include <ziti/ziti_tunnel_cbs.h>
#include "ziti/ziti_log.h"
#include "sdk.h"

void doZitiShutdown(uv_async_t *handle);
void zitiContextEvent(ziti_context nf, int status, void *ctx);
void eventCB(ziti_context ztx, ziti_event_t *event);

void shutdown_callback(uv_async_t *handle);
void free_async(uv_handle_t* timer);

void c_mapiter(model_map *map);
void ziti_dump_to_file(void *ctx, char* outputPath);
int ziti_dump_to_log(void *ctx, void* stringsBuilder);
void* stailq_first_forgo(void* entries);

protocol_t* stailq_first_protocol(tunneled_service_t* ts);
address_t* stailq_first_address(tunneled_service_t* ts);
port_range_t* stailq_first_port_range(tunneled_service_t* ts);

protocol_t* stailq_next_protocol(protocol_t* cur);
address_t* stailq_next_address(address_t* cur);
port_range_t* stailq_next_port_range(port_range_t* cur);

*/
import "C"
import (
	"errors"
	"github.com/openziti/desktop-edge-win/service/ziti-tunnel/api"
	"github.com/openziti/desktop-edge-win/service/ziti-tunnel/dto"
	"github.com/openziti/desktop-edge-win/service/ziti-tunnel/util/logging"
	"os"
	"strings"
	"sync"
	"unsafe"
)

const (
	ADDED   = "added"
	REMOVED = "removed"
)

var log = logging.Logger()
var noFileLog = logging.NoFilenameLogger()
var Version dto.ServiceVersion
var BulkServiceChanges = make(chan BulkServiceChange, 32)

type sdk struct {
	libuvCtx *C.libuv_ctx
}
type ServiceChange struct {
	Operation   string
	Service     *ZService
	ZitiContext *ZIdentity
}
type BulkServiceChange struct {
	Fingerprint       string
	HostnamesToAdd    map[string]bool
	HostnamesToRemove map[string]bool
	ServicesToRemove  []*dto.Service
	ServicesToAdd     []*dto.Service
}

var _impl sdk

func init() {
	_impl.libuvCtx = (*C.libuv_ctx)(C.calloc(1, C.sizeof_libuv_ctx))
	C.libuv_init(_impl.libuvCtx)
}

func SetLogLevel(level int) {
	log.Infof("Setting cziti log level to: %d", level)
	C.set_log_level(C.int(level), _impl.libuvCtx)
}

func Start(a api.DesktopEdgeIface, ip string, maskBits int, loglevel int) {
	goapi = a
	DnsInit(ip, maskBits)
	appInfo := "Ziti Desktop Edge for Windows"
	log.Debugf("informing c sdk of appinfo: %s at %s", appInfo, Version.Version)
	C.ziti_set_app_info(C.CString(appInfo), C.CString(Version.Version))
	v := C.ziti_get_version()
	log.Infof("starting ziti-sdk-c %s(%s)[%s]", C.GoString(v.version), C.GoString(v.revision), C.GoString(v.build_date))

	_impl.run(loglevel)
}

func (inst *sdk) run(loglevel int) {
	SetLogLevel(loglevel)
	C.libuv_run(inst.libuvCtx)
}

type ZService struct {
	Name    string
	Id      string
	Service *dto.Service
	Czctx   C.ziti_context
}

type ZIdentity struct {
	Options       *C.ziti_options
	czctx         C.ziti_context
	czid          *C.ziti_identity
	status        int
	statusErr     error
	Loaded        bool
	Name          string
	Version       string
	Services      sync.Map
	Fingerprint   string
	Active        bool
	StatusChanges func(int)
	MfaNeeded     bool
	MfaEnabled    bool
	mfa           *Mfa
}

type Mfa struct {
	mfaContext unsafe.Pointer
	authQuery  *C.ziti_auth_query_mfa
	responseCb C.ziti_ar_mfa_cb
}

func NewZid(statusChange func(int)) *ZIdentity {
	zid := &ZIdentity{}
	zid.Services = sync.Map{}
	zid.Options = (*C.ziti_options)(C.calloc(1, C.sizeof_ziti_options))
	zid.StatusChanges = statusChange
	return zid
}

func (c *ZIdentity) GetMetrics() (int64, int64, bool) {
	if c == nil {
		return 0, 0, false
	}
	if unsafe.Pointer(c.czctx) == C.NULL {
		log.Debugf("ziti context is still null. the identity is probably not initialized yet: %s", c.Fingerprint)
		return 0, 0, false
	}
	var up, down C.double
	C.ziti_get_transfer_rates(c.czctx, &up, &down)

	return int64(up), int64(down), true
}

func (c *ZIdentity) UnsafePointer() unsafe.Pointer {
	return unsafe.Pointer(c.czctx)
}
func (c *ZIdentity) AsKey() string {
	return "marker askey"
}

func (c *ZIdentity) Status() (int, error) {
	return c.status, c.statusErr
}

func (c *ZIdentity) setVersionFromId() string {
	if len(c.Version) > 0 {
		return c.Version
	}
	c.Version = "<unknown version>"
	if c != nil {
		if c.czctx != nil {
			v1 := C.ziti_get_controller_version(c.czctx)
			return C.GoString(v1.version)
		}
	}
	return c.Version
}

func (c *ZIdentity) setNameFromId() string {
	if len(c.Name) > 0 {
		return c.Name
	}
	c.Name = "<unknown>"
	if c != nil {
		if c.czid != nil {
			if c.czid.name != nil {
				c.Name = C.GoString(c.czid.name)
			} else {
				log.Debug("in Name - c.zid.name was nil")
			}
		} else {
			log.Debug("in Name - c.zid was nil")
		}
	} else {
		log.Debug("in Name - c was nil")
	}
	return c.Name
}

func (c *ZIdentity) Tags() []string {
	if c.czctx != nil && c.czid != nil {
		C.c_mapiter(&c.czid.tags)
		/*
			it := C.model_map_iterator(&c.zid.tags)
			for {
				if it != nil {
					k := C.model_map_it_value(it)
					v := C.model_map_it_value(it)
					log.Infof("key: %s. value: %s", k, v)
				} else {
					break
				}
				it = C.model_map_it_next(it) //get the next entry
			}
			return nil*/
	}
	return nil
}

func (c *ZIdentity) Controller() string {
	if c.czctx != nil {
		return C.GoString(C.ziti_get_controller(c.czctx))
	}
	return C.GoString(c.Options.controller)
}

func (zid *ZIdentity) Shutdown() {

	async := (*C.uv_async_t)(C.malloc(C.sizeof_uv_async_t))
	async.data = unsafe.Pointer(zid.czctx)
	log.Debugf("setting up call to ziti_shutdown for context %p using uv_async_t", async.data)
	C.uv_async_init(_impl.libuvCtx.l, async, C.uv_async_cb(C.doZitiShutdown))
	C.uv_async_send((*C.uv_async_t)(unsafe.Pointer(async)))
}

//export doZitiShutdown
func doZitiShutdown(async *C.uv_async_t) {
	ctx := C.ziti_context(async.data)
	log.Infof("invoking ziti_shutdown for context %p", &ctx)
	C.ziti_shutdown(ctx)
}

func serviceCB(ziti_ctx C.ziti_context, service *C.ziti_service, status C.int, zid *ZIdentity) *dto.Service {
	if zid == nil {
		log.Errorf("in serviceCB with nil zid??? ")
		return nil
	}

	name := C.GoString(service.name)
	svcId := C.GoString(service.id)
	log.Debugf("============ INSIDE serviceCB - status: %s:%s - %v, %v ============", name, svcId, status, service.perm_flags)
	ts := C.ziti_sdk_c_on_service(ziti_ctx, service, status, unsafe.Pointer(theTun.tunCtx))

	protocols := getTunneledServiceProtocols(ts)
	addresses := getTunneledServiceAddresses(ts)
	portRanges := getTunneledServicePortRanges(ts)
	log.Infof("service update: %s, id: %s, portocols:%s, addresses:%v, portRanges: %v", name, svcId, protocols, addresses, portRanges)

	var svc *dto.Service

	if status == C.ZITI_SERVICE_UNAVAILABLE {
		log.Debugf("serivce has become unavailalbe: %s [%s]", name, svcId)
		f, ok := zid.Services.Load(svcId)
		if ok {
			svc = &dto.Service{
				Name: name,
				Id:   svcId,
			}
			found := f.(*ZService)
			if found != nil {
				found.Service = nil
			}
			zid.Services.Delete(svcId)
		} else {
			log.Warnf("could not remove service? service not found with id: %s, name: %s in context %d", svcId, name, &zid)
		}
	} else if status == C.ZITI_OK {
		pcIds := make(map[string]bool)
		var postureChecks []dto.PostureCheck

		//if any posture query sets pass - that will grant the user access to that service
		hasAccess := false

		//find all posture checks sets...
		for setIdx := 0; true; setIdx++ {
			pqs := C.posture_query_set_get(service.posture_query_set, C.int(setIdx))
			if unsafe.Pointer(pqs) == C.NULL {
				break
			}

			if bool(pqs.is_passing) {
				hasAccess = true
			}

			//get all posture checks in this set...
			for pqIdx := 0; true; pqIdx++ {
				pq := C.posture_queries_get(pqs.posture_queries, C.int(pqIdx))
				if unsafe.Pointer(pq) == C.NULL {
					break
				}

				var pcId string
				pcId = C.GoString(pq.id)

				_, found := pcIds[pcId]
				if found {
					log.Tracef("posture check with id %s already in failing posture check map", pcId)
				} else {
					pcIds[C.GoString(pq.id)] = false
					pc := dto.PostureCheck{
						IsPassing: bool(pq.is_passing),
						QueryType: C.GoString(pq.query_type),
						Id:        pcId,
					}

					postureChecks = append(postureChecks, pc)
				}
			}
		}

		svc = &dto.Service{
			Name:          name,
			Id:            svcId,
			Protocols:     protocols,
			Addresses:     addresses,
			Ports:         portRanges,
			OwnsIntercept: true,
			PostureChecks: postureChecks,
			IsAccessable:  hasAccess,
		}
		added := ZService{
			Name:    name,
			Id:      svcId,
			Service: svc,
			Czctx:   ziti_ctx,
		}
		zid.Services.Store(svcId, &added)
	}

	return svc
}

//export eventCB
func eventCB(ztx C.ziti_context, event *C.ziti_event_t) {
	log.Tracef("events received. type: %d for ztx(%p)", event._type, ztx)

	appCtx := C.ziti_app_ctx(ztx)
	isCnull := appCtx == C.NULL
	isNil := appCtx == nil
	if isCnull || isNil {
		log.Errorf("in eventCB with null ziti_app_ctx??? ")
		return
	}

	zid := (*ZIdentity)(appCtx)

	switch event._type {
	case C.ZitiContextEvent:
		ctxEvent := C.ziti_event_context_event(event)
		zitiContextEvent(ztx, ctxEvent.ctrl_status, zid)

	case C.ZitiRouterEvent:
		rtrEvent := C.ziti_event_router_event(event)
		switch rtrEvent.status {
		case C.EdgeRouterConnected:
			log.Infof("router[%s]: connected to %s", C.GoString(rtrEvent.name), C.GoString(rtrEvent.version))
		case C.EdgeRouterDisconnected:
			log.Infof("router[%s]: Disconnected", C.GoString(rtrEvent.name))
		case C.EdgeRouterRemoved:
			log.Infof("router[%s]: Removed", C.GoString(rtrEvent.name))
		case C.EdgeRouterUnavailable:
			log.Infof("router[%s]: Unavailable", C.GoString(rtrEvent.name))
		}

	case C.ZitiServiceEvent:
		hostnamesToAdd := make(map[string]bool)
		hostnamesToRemove := make(map[string]bool)
		servicesToRemove := make([]*dto.Service, 0)
		servicesToAdd := make([]*dto.Service, 0)

		srvEvent := C.ziti_event_service_event(event)

		for i := 0; true; i++ {
			removed := C.ziti_service_array_get(srvEvent.removed, C.int(i))
			if unsafe.Pointer(removed) == C.NULL {
				break
			}
			svcToRemove := serviceCB(ztx, removed, C.ZITI_SERVICE_UNAVAILABLE, zid)
			if svcToRemove != nil {
				remAddys := svcToRemove.Addresses
				for _, toRemove := range remAddys {
					if toRemove.IsHost {
						hostnamesToRemove[toRemove.HostName] = true
					}
				}
				servicesToRemove = append(servicesToRemove, svcToRemove)
			}
		}
		for i := 0; true; i++ {
			changed := C.ziti_service_array_get(srvEvent.changed, C.int(i))
			if unsafe.Pointer(changed) == C.NULL {
				break
			}
			log.Info("service changed remove the service then add it back immediately", C.GoString(changed.name))
			svcToRemove := serviceCB(ztx, changed, C.ZITI_SERVICE_UNAVAILABLE, zid)
			if svcToRemove != nil {
				remAddys := svcToRemove.Addresses
				for _, toRemove := range remAddys {
					if toRemove.IsHost {
						hostnamesToRemove[toRemove.HostName] = true
					}
				}
				servicesToRemove = append(servicesToRemove, svcToRemove)
			}

			svcToAdd := serviceCB(ztx, changed, C.ZITI_OK, zid)
			if svcToAdd != nil {
				addAddys := svcToAdd.Addresses
				for _, toAdd := range addAddys {
					if toAdd.IsHost {
						hostnamesToAdd[toAdd.HostName] = true
					}
				}
				servicesToAdd = append(servicesToAdd, svcToAdd)
			}
		}
		for i := 0; true; i++ {
			added := C.ziti_service_array_get(srvEvent.added, C.int(i))
			if unsafe.Pointer(added) == C.NULL {
				break
			}
			svcToAdd := serviceCB(ztx, added, C.ZITI_OK, zid)
			if svcToAdd != nil {
				addAddys := svcToAdd.Addresses
				for _, toAdd := range addAddys {
					if toAdd.IsHost {
						hostnamesToAdd[toAdd.HostName] = true
					}
				}
				servicesToAdd = append(servicesToAdd, svcToAdd)
			}
		}

		svcChange := BulkServiceChange{
			Fingerprint:       zid.Fingerprint,
			HostnamesToAdd:    hostnamesToAdd,
			HostnamesToRemove: hostnamesToRemove,
			ServicesToRemove:  servicesToRemove,
			ServicesToAdd:     servicesToAdd,
		}

		if len(BulkServiceChanges) == cap(BulkServiceChanges) {
			log.Warn("Service changes are not being processed fast enough. This client is out of date from the controller! This is unexpected. If you see this warning please report")
		} else {
			BulkServiceChanges <- svcChange
		}
	default:
		log.Infof("event %d not handled", event._type)
	}
}

func zitiContextEvent(ztx C.ziti_context, status C.int, zid *ZIdentity) {

	zid.status = int(status)
	zid.statusErr = zitiError(status)
	zid.czctx = ztx

	cfg := C.GoString(zid.Options.config)

	if status == C.ZITI_OK {
		if ztx != nil {
			zid.czid = C.ziti_get_identity(ztx)
		}

		zid.Name = zid.setNameFromId()
		zid.Version = zid.setVersionFromId()
		log.Debugf("============ controller connected: %s at %v. MFA: %v", zid.Name, zid.Version, zid.MfaEnabled)
	} else {
		log.Errorf("zitiContextEvent failed to connect[%s] to controller for %s", zid.statusErr, cfg)
	}
	zid.StatusChanges(int(status))
	idMap.Store(ztx, zid)
	log.Debugf("zitiContextEvent triggered and stored in ZIdentity with pointer: %p", unsafe.Pointer(ztx))
}

func zitiError(code C.int) error {
	if int(code) != 0 {
		return errors.New(C.GoString(C.ziti_errorstr(code)))
	}
	return nil
}

func LoadZiti(zid *ZIdentity, cfg string, refreshInterval int) {
	zid.Options.config = C.CString(cfg)
	zid.Options.refresh_interval = C.long(refreshInterval)
	zid.Options.metrics_type = C.INSTANT
	zid.Options.config_types = C.all_configs
	zid.Options.pq_domain_cb = C.ziti_pq_domain_cb(C.ziti_pq_domain_go)
	zid.Options.pq_mac_cb = C.ziti_pq_mac_cb(C.ziti_pq_mac_go)
	zid.Options.pq_os_cb = C.ziti_pq_os_cb(C.ziti_pq_os_go)
	zid.Options.pq_process_cb = C.ziti_pq_process_cb(C.ziti_pq_process_go)

	zid.Options.events = C.ZitiContextEvent | C.ZitiServiceEvent | C.ZitiRouterEvent
	zid.Options.event_cb = C.ziti_event_cb(C.eventCB)

	zid.Options.aq_mfa_cb = C.ziti_aq_mfa_cb(C.ziti_aq_mfa_cb_go)

	ptr := unsafe.Pointer(zid)
	zid.Options.app_ctx = ptr

	rc := C.ziti_init_opts(zid.Options, _impl.libuvCtx.l)
	if rc != C.ZITI_OK {
		zid.status, zid.statusErr = int(rc), zitiError(rc)
		log.Errorf("FAILED to load identity from config file: %s due to: %s", cfg, zid.statusErr)
	} else {
		log.Debugf("successfully loaded identity from config file: %s", cfg)
	}
}

//export free_async
func free_async(handle *C.uv_handle_t) {
	C.free(unsafe.Pointer(handle))
}

//export log_writer_cb
func log_writer_cb(level C.int, loc C.string, msg C.string, msglen C.int) {
	gomsg := C.GoStringN(msg, msglen)
	goline := C.GoString(loc)
	lvl := level
	switch lvl {
	case 0:
		noFileLog.Warnf("SDK_: level 0 should not be logged, please report: %s", gomsg)
		break
	case 1:
		noFileLog.Errorf("SDKe: %s\t%s", goline, gomsg)
		break
	case 2:
		noFileLog.Warnf("SDKw: %s\t%s", goline, gomsg)
		break
	case 3:
		noFileLog.Infof("SDKi: %s\t%s", goline, gomsg)
		break
	case 4:
		noFileLog.Debugf("SDKd: %s\t%s", goline, gomsg)
		break
	case 5:
		//VERBOSE:5
		noFileLog.Tracef("SDKv: %s\t%s", goline, gomsg)
		break
	case 6:
		//TRACE:6
		noFileLog.Tracef("SDKt: %s\t%s", goline, gomsg)
		break
	default:
		noFileLog.Warnf("SDK_: level [%d] NOT recognized: %s", level, gomsg)
		break
	}
}

//export ziti_dump_go_to_file_cb
func ziti_dump_go_to_file_cb(outputPath *C.char, charData *C.char) {
	f, err := os.OpenFile(C.GoString(outputPath),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Warnf("unexpect error opening file: %v", err)
		_ = f.Close()
		return
	}
	_, _ = f.WriteString(C.GoString(charData))
	_ = f.Close()
}

func ZitiDump(zid *ZIdentity, path string) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))

	e := os.Remove(path)
	if e != nil {
		//probably did not exist
		log.Debugf("Could not remove file at: %s", path)
	} else {
		log.Debugf("Removed existing ziti_dump file at: %s", path)
	}

	C.ziti_dump_to_file(unsafe.Pointer(zid.czctx), cpath)
	log.Infof("ziti_dump saved to: %s", path)
}

//export ziti_dump_go_to_log_cb
func ziti_dump_go_to_log_cb(stringsBuilder unsafe.Pointer, charData *C.char) {
	sb := (*strings.Builder)(stringsBuilder)
	sb.WriteString(C.GoString(charData))
}
func ZitiDumpOnShutdown(zid *ZIdentity, sb *strings.Builder) {
	C.ziti_dump_to_log(unsafe.Pointer(zid.czctx), unsafe.Pointer(sb))
}

func getTunneledServiceProtocols(ts *C.tunneled_service_t) []string {
	var protocols []string
	next := C.stailq_first_protocol(ts)
	if unsafe.Pointer(next) != C.NULL {
		protocols = append(protocols, C.GoString(next.protocol))
		for {
			next = C.stailq_next_protocol(next)
			if unsafe.Pointer(next) != C.NULL {
				protocols = append(protocols, C.GoString(next.protocol))
			} else {
				break
			}
		}
	}
	return protocols
}

func getTunneledServicePortRanges(ts *C.tunneled_service_t) []dto.PortRange {
	var values []dto.PortRange
	next := C.stailq_first_port_range(ts)
	if unsafe.Pointer(next) != C.NULL {
		p := dto.PortRange{
			High: int(next.high),
			Low:  int(next.low),
		}
		values = append(values, p)
		for {
			next = C.stailq_next_port_range(next)
			if unsafe.Pointer(next) != C.NULL {
				p := dto.PortRange{
					High: int(next.high),
					Low:  int(next.low),
				}
				values = append(values, p)
			} else {
				break
			}
		}
	}
	return values
}

func getTunneledServiceAddresses(ts *C.tunneled_service_t) []dto.Address {
	var values []dto.Address
	next := C.stailq_first_address(ts)
	if unsafe.Pointer(next) != C.NULL {
		p := dto.Address{
			IsHost:   bool(next.is_hostname),
			HostName: C.GoString(&next.str[0]),
			IP:       C.GoString(C.ipaddr_ntoa(&next.ip)),
			Prefix:   int(next.prefix_len),
		}
		values = append(values, p)
		for {
			next = C.stailq_next_address(next)
			if unsafe.Pointer(next) != C.NULL {
				p := dto.Address{
					IsHost:   bool(next.is_hostname),
					HostName: C.GoString(&next.str[0]),
					IP:       C.GoString(C.ipaddr_ntoa(&next.ip)),
					Prefix:   int(next.prefix_len),
				}
				values = append(values, p)
			} else {
				break
			}
		}
	}
	return values
}

func InitTunnelerDns(ipBase uint32, mask int) {
	C.ziti_tunneler_init_dns(C.uint32_t(ipBase), C.int(mask))
}

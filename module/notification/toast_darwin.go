//go:build darwin && !server

package notification

import (
	"context"
	"sync"

	"github.com/ebitengine/purego/objc"
	"github.com/google/uuid"
	"github.com/tradalab/scorix/internal/mac"
	"github.com/tradalab/scorix/logger"
)

// UserNotifications option bits, spelled out because purego has no headers.
const (
	authAlert, authSound uint64 = 1 << 2, 1 << 1 // UNAuthorizationOptions
	// UNNotificationPresentationOptions, the macOS 11 spelling. Without them a
	// notification raised while the app is frontmost goes straight to Notification
	// Center and the user never sees it.
	presentBanner, presentSound uint64 = 1 << 4, 1 << 1
)

// A click comes back through the delegate, so unlike Windows and Linux this
// needs no URL scheme - and the name and icon come from the .app bundle, which
// is why both of those arguments go unused.
func showToast(_ context.Context, _ appInfo, req NotifyRequest, _ string) error {
	if err := mac.Init(); err != nil {
		return err
	}
	if mac.BundleID() == "" {
		// The `go run` and `scorix dev` case. See mac.BundleID for why this is a
		// check and not a recovered error.
		return zenityNotify(req)
	}
	// Not awaited: any click arrives later through the delegate anyway, so there
	// is nothing to return here and failures are logged instead.
	mac.DispatchMain(func() {
		defer mac.Recover("showToast")
		center := notificationCenter()
		if center == 0 {
			return
		}
		// Repeated from prepare because IPC is live from OnLoad: a Notify arriving
		// before OnStart would otherwise post with no delegate. Both use a Once.
		attachDelegate(center)
		requestAuth(center)
		post(center, req)
	})
	return nil
}

// prepare moves the delegate and the permission prompt to startup. Apple wants
// the delegate set before the app finishes launching or a click that RELAUNCHED
// the app is not delivered, and requestAuthorization is asynchronous while post
// does not wait, so the very first Notify would otherwise race the prompt.
func prepare() {
	if err := mac.Init(); err != nil || mac.BundleID() == "" {
		return
	}
	mac.DispatchMain(func() {
		defer mac.Recover("prepareNotifications")
		if center := notificationCenter(); center != 0 {
			attachDelegate(center)
			requestAuth(center)
		}
	})
}

func notificationCenter() objc.ID {
	c := mac.Class("UNUserNotificationCenter").Send(mac.Sel("currentNotificationCenter"))
	if c == 0 {
		logger.Error("[notification] no UNUserNotificationCenter")
	}
	return c
}

func clickable(req NotifyRequest, _ string) bool {
	if err := mac.Init(); err != nil || mac.BundleID() == "" {
		return false // the zenity fallback reports nothing back
	}
	return req.ID != ""
}

func post(center objc.ID, req NotifyRequest) {
	content := mac.Class("UNMutableNotificationContent").Send(mac.Sel("alloc")).Send(mac.Sel("init"))
	content.Send(mac.Sel("setTitle:"), mac.NSString(req.Title))
	content.Send(mac.Sel("setBody:"), mac.NSString(req.Text))
	content.Send(mac.Sel("setSound:"),
		mac.Class("UNNotificationSound").Send(mac.Sel("defaultSound")))

	if id := categoryFor(center, req.Actions); id != "" {
		content.Send(mac.Sel("setCategoryIdentifier:"), mac.NSString(id))
	}

	// An empty identifier makes every notification replace the previous one, so
	// a fire-and-forget caller gets a stable-but-unique one instead.
	ident := req.ID
	if ident == "" {
		ident = "scorix-" + uuid.NewString()
	}
	// A nil trigger means deliver now.
	r := mac.Class("UNNotificationRequest").Send(mac.Sel("requestWithIdentifier:content:trigger:"),
		mac.NSString(ident), content, objc.ID(0))
	// nil completion handler is explicitly allowed here.
	center.Send(mac.Sel("addNotificationRequest:withCompletionHandler:"), r, objc.ID(0))
	content.Send(mac.Sel("release"))
}

var (
	catMu sync.Mutex
	cats  = map[string]objc.ID{}
)

// categoryFor returns the category identifier carrying these buttons, registering
// it the first time it is seen. setNotificationCategories REPLACES the whole set,
// so every category ever registered is re-sent each time.
func categoryFor(center objc.ID, as []Action) string {
	if len(as) == 0 {
		return ""
	}
	id := categoryID(as)

	catMu.Lock()
	defer catMu.Unlock()
	if _, ok := cats[id]; ok {
		return id
	}
	acts := mac.Class("NSMutableArray").Send(mac.Sel("array"))
	for _, a := range as {
		act := mac.Class("UNNotificationAction").Send(
			mac.Sel("actionWithIdentifier:title:options:"),
			mac.NSString(a.Key), mac.NSString(a.Label), uint64(0))
		acts.Send(mac.Sel("addObject:"), act)
	}
	cat := mac.Class("UNNotificationCategory").Send(
		mac.Sel("categoryWithIdentifier:actions:intentIdentifiers:options:"),
		mac.NSString(id), acts, mac.Class("NSArray").Send(mac.Sel("array")), uint64(0))
	// The constructor returns an autoreleased object and this map outlives the
	// pool: without the retain, the next call sends addObject: to freed memory.
	cat.Send(mac.Sel("retain"))
	cats[id] = cat

	set := mac.Class("NSMutableSet").Send(mac.Sel("set"))
	for _, c := range cats {
		set.Send(mac.Sel("addObject:"), c)
	}
	center.Send(mac.Sel("setNotificationCategories:"), set)
	return id
}

var authOnce sync.Once

func requestAuth(center objc.ID) {
	authOnce.Do(func() {
		// void (^)(BOOL granted, NSError *error). granted arrives in the low byte
		// of its register, so it is read as a word and masked.
		block, err := mac.NewBlock(func(_ uintptr, granted uintptr, _ uintptr) uintptr {
			defer mac.Recover("requestAuthorization")
			if granted&1 == 0 {
				logger.Warn("[notification] the user has not allowed notifications for this app")
			}
			return 0
		})
		if err != nil {
			logger.Error("[notification] cannot build the authorization callback", "err", err)
			return
		}
		mac.SendOptionsBlock(center, mac.Sel("requestAuthorizationWithOptions:completionHandler:"),
			authAlert|authSound, block)
	})
}

var (
	delegateOnce sync.Once
	delegateObj  objc.ID
)

func attachDelegate(center objc.ID) {
	delegateOnce.Do(func() {
		_, err := objc.RegisterClass(
			"ScorixNotificationDelegate",
			objc.GetClass("NSObject"),
			[]*objc.Protocol{objc.GetProtocol("UNUserNotificationCenterDelegate")},
			nil,
			[]objc.MethodDef{
				{
					Cmd: mac.Sel("userNotificationCenter:willPresentNotification:withCompletionHandler:"),
					Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID, _ objc.ID, handler objc.ID) {
						defer mac.Recover("willPresentNotification")
						mac.InvokeBlockUint(uintptr(handler), presentBanner|presentSound)
					},
				},
				{
					Cmd: mac.Sel("userNotificationCenter:didReceiveResponse:withCompletionHandler:"),
					Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID, response objc.ID, handler objc.ID) {
						defer mac.Recover("didReceiveResponse")
						id := mac.GoString(response.
							Send(mac.Sel("notification")).
							Send(mac.Sel("request")).
							Send(mac.Sel("identifier")))
						action, ok := responseAction(
							mac.GoString(response.Send(mac.Sel("actionIdentifier"))))
						if ok && id != "" {
							deliver(id, action)
						}
						// The system treats the delegate as busy until this runs.
						mac.InvokeVoidBlock(uintptr(handler))
					},
				},
			},
		)
		if err != nil {
			logger.Error("[notification] cannot register the delegate", "err", err)
			return
		}
		// .delegate is a WEAK property, so this reference is the only thing keeping
		// the object alive; dropping it leaves the center pointing at freed memory.
		delegateObj = mac.Class("ScorixNotificationDelegate").Send(mac.Sel("new"))
		center.Send(mac.Sel("setDelegate:"), delegateObj)
	})
}

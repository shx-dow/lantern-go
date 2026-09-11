//go:build darwin

package tray

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

// TrayHandler forwards menu-bar clicks into Go.
@interface TrayHandler : NSObject
@end
@implementation TrayHandler
- (void)clicked:(id)sender { trayClicked(); }
@end

void trayClicked(void);

static NSStatusItem *trayItem;
static TrayHandler *trayHandler;

static void trayInit(const char *title) {
	[NSApplication sharedApplication];
	[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
	NSStatusBar *bar = [NSStatusBar systemStatusBar];
	trayItem = [bar statusItemWithLength:NSVariableStatusItemLength];
	[trayItem setHighlightMode:YES];
	trayHandler = [[TrayHandler alloc] init];
	NSButton *button = [trayItem button];
	[button setTitle:[NSString stringWithUTF8String:title]];
	[button setTarget:trayHandler];
	[button setAction:@selector(clicked:)];
}

static void trayRun(void) {
	[NSApp run];
}

static void trayQuit(void) {
	[NSApp terminate:nil];
}
*/
import "C"
import "unsafe"

// NOTE: this file needs macOS + Xcode command-line tools to compile or
// run; it is not verifiable from Linux/Windows. A macOS CI lane should run
// `go build ./...` and click-test the icon before release.

// Available is always true on macOS; a graphical session is assumed.
func Available() bool { return true }

var trayURL string

//export trayClicked
func trayClicked() {
	_ = openBrowser(trayURL)
}

// Run installs the menu-bar item and enters the AppKit loop until Quit.
// Must run on the main OS thread (lanternd calls it from main).
func Run(cfg Config) error {
	if stopped() {
		return nil
	}
	trayURL = cfg.UIURL
	title := C.CString(cfg.Title)
	defer C.free(unsafe.Pointer(title))
	go func() {
		<-stopCh
		C.trayQuit()
	}()
	C.trayInit(title)
	C.trayRun()
	return nil
}

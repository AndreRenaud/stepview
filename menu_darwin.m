// The File and View menus in the macOS menu bar. Go describes them; the
// actions are sent back to Go as command numbers.

#import <Cocoa/Cocoa.h>
#include <stdatomic.h>

extern void stepviewMenuCommand(int cmd);

// Bit masks of the enabled and checked commands, set from the game loop and
// read on the main thread when a menu is shown or a shortcut typed.
static _Atomic unsigned int enabledMask, checkedMask;

@interface StepviewMenuTarget : NSObject <NSMenuItemValidation>
@end

@implementation StepviewMenuTarget
- (void)perform:(NSMenuItem *)item {
	stepviewMenuCommand((int)[item tag]);
}

- (BOOL)validateMenuItem:(NSMenuItem *)item {
	unsigned int bit = 1u << [item tag];
	[item setState:(atomic_load(&checkedMask) & bit) ? NSControlStateValueOn : NSControlStateValueOff];
	return (atomic_load(&enabledMask) & bit) != 0;
}
@end

static StepviewMenuTarget *target;
static NSMutableArray<NSMenuItem *> *topItems;

void *stepviewMenuNew(const char *title) {
	NSMenu *menu = [[NSMenu alloc] initWithTitle:[NSString stringWithUTF8String:title]];
	return (void *)menu;
}

void stepviewMenuAddItem(void *menu, int cmd, const char *title, const char *key, unsigned long mods) {
	if (target == nil) {
		target = [[StepviewMenuTarget alloc] init];
	}
	NSMenuItem *item = [[NSMenuItem alloc] initWithTitle:[NSString stringWithUTF8String:title]
	                                              action:@selector(perform:)
	                                       keyEquivalent:[NSString stringWithUTF8String:key]];
	[item setKeyEquivalentModifierMask:(NSEventModifierFlags)mods];
	[item setTag:cmd];
	[item setTarget:target];
	[(NSMenu *)menu addItem:item];
	[item release];
}

void stepviewMenuAddSeparator(void *menu) {
	[(NSMenu *)menu addItem:[NSMenuItem separatorItem]];
}

void stepviewMenuAddSubmenu(void *menu, void *sub) {
	NSMenuItem *item = [[NSMenuItem alloc] initWithTitle:[(NSMenu *)sub title] action:nil keyEquivalent:@""];
	[item setSubmenu:(NSMenu *)sub];
	[(NSMenu *)menu addItem:item];
	[item release];
	[(NSMenu *)sub release];
}

// stepviewMenuAddTop queues a menu for the menu bar.
void stepviewMenuAddTop(void *menu) {
	if (topItems == nil) {
		topItems = [[NSMutableArray alloc] init];
	}
	NSMenuItem *item = [[NSMenuItem alloc] initWithTitle:[(NSMenu *)menu title] action:nil keyEquivalent:@""];
	[item setSubmenu:(NSMenu *)menu];
	[topItems addObject:item];
	[item release];
	[(NSMenu *)menu release];
}

// stepviewMenuInstall puts the queued menus into the menu bar, after the
// application menu, once the windowing code has made the menu bar while the
// application finishes launching.
void stepviewMenuInstall(void) {
	[[NSNotificationCenter defaultCenter] addObserverForName:NSApplicationDidFinishLaunchingNotification
	                                                  object:nil
	                                                   queue:nil
	                                              usingBlock:^(NSNotification *note) {
		NSMenu *bar = [NSApp mainMenu];
		if (bar == nil) {
			bar = [[[NSMenu alloc] init] autorelease];
			[NSApp setMainMenu:bar];
		}
		NSInteger at = MIN((NSInteger)1, [bar numberOfItems]);
		for (NSMenuItem *item in topItems) {
			[bar insertItem:item atIndex:at++];
		}
	}];
}

void stepviewMenuSetState(unsigned int enabled, unsigned int checked) {
	atomic_store(&enabledMask, enabled);
	atomic_store(&checkedMask, checked);
}

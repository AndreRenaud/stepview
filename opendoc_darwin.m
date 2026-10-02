// Delivery of Finder's "open documents" Apple Event (a double-click on a
// model, or a drop onto the Dock icon) to Go.

#import <Cocoa/Cocoa.h>

extern void stepviewOpenDocument(char *path);

@interface StepviewOpenHandler : NSObject
@end

@implementation StepviewOpenHandler
- (void)openDocuments:(NSAppleEventDescriptor *)event withReplyEvent:(NSAppleEventDescriptor *)reply {
	NSAppleEventDescriptor *list = [[event paramDescriptorForKeyword:keyDirectObject] coerceToDescriptorType:typeAEList];
	for (NSInteger i = 1; i <= [list numberOfItems]; i++) {
		NSURL *url = [[list descriptorAtIndex:i] fileURLValue];
		if ([url isFileURL]) {
			stepviewOpenDocument((char *)[url fileSystemRepresentation]);
		}
	}
}
@end

void stepviewInstallOpenHandler(void) {
	static StepviewOpenHandler *handler;
	handler = [[StepviewOpenHandler alloc] init];
	// NSApplication installs its own handler for the event while launching,
	// and delivers a launch-time event right after this notification. A
	// handler installed here replaces the default one and still sees it.
	[[NSNotificationCenter defaultCenter] addObserverForName:NSApplicationWillFinishLaunchingNotification
	                                                  object:nil
	                                                   queue:nil
	                                              usingBlock:^(NSNotification *note) {
		[[NSAppleEventManager sharedAppleEventManager] setEventHandler:handler
		                                                   andSelector:@selector(openDocuments:withReplyEvent:)
		                                                 forEventClass:kCoreEventClass
		                                                    andEventID:kAEOpenDocuments];
	}];
}

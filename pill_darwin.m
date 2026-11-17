#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>

// Where item titles start in a menu without a checkmark column, and where
// submenu arrows end, both from the row's edges (measured on macOS 27).
static const CGFloat titleInset = 14;
static const CGFloat endInset = 20;

@interface TailtrayPill : NSView
@property BOOL on;
@end

@implementation TailtrayPill
// Drawn by hand: NSSwitch shows its on state gray unless its app is active,
// and a menu bar app is not active while its menu is open.
- (void)drawRect:(NSRect)dirtyRect {
	CGFloat radius = NSHeight(self.bounds) / 2;
	[(self.on ? [NSColor controlAccentColor] : [NSColor tertiaryLabelColor]) setFill];
	[[NSBezierPath bezierPathWithRoundedRect:self.bounds xRadius:radius yRadius:radius] fill];
	CGFloat knob = NSHeight(self.bounds) - 4;
	CGFloat x = self.on ? NSMaxX(self.bounds) - knob - 2 : 2;
	[[NSColor whiteColor] setFill];
	[[NSBezierPath bezierPathWithOvalInRect:NSMakeRect(x, 2, knob, knob)] fill];
}
@end

@interface TailtrayRow : NSView
@property (weak) NSMenuItem *item;
@end

@implementation TailtrayRow
// A click anywhere on the row sends the replaced item's own action, so the
// tray library handles it as before. The switch is not flipped here: the menu
// rebuilt after the change shows the state Tailscale actually reached.
- (void)mouseUp:(NSEvent *)event {
	if (!self.item.isEnabled) {
		return;
	}
	[NSApp sendAction:self.item.action to:self.item.target from:self.item];
}
@end

void tailtray_pill(const char *ctitle, int on) {
	NSString *title = [NSString stringWithUTF8String:ctitle];
	void (^swap)(void) = ^{
		// The tray library keeps its menu in a private ivar. If that ever
		// changes, the item is left alone and keeps its checkmark.
		id delegate = [NSApp delegate];
		Ivar ivar = class_getInstanceVariable([delegate class], "menu");
		if (ivar == NULL) {
			return;
		}
		id menu = object_getIvar(delegate, ivar);
		if (![menu isKindOfClass:[NSMenu class]]) {
			return;
		}
		NSMenuItem *item = [menu itemWithTitle:title];
		if (item == nil) {
			return;
		}

		NSTextField *label = [NSTextField labelWithString:title];
		label.font = [NSFont menuFontOfSize:0];
		label.textColor = item.isEnabled ? [NSColor labelColor] : [NSColor disabledControlTextColor];
		[label sizeToFit];
		TailtrayPill *pill = [[TailtrayPill alloc] initWithFrame:NSMakeRect(0, 0, 32, 18)];
		pill.on = on;

		CGFloat height = MAX(NSHeight(label.frame), NSHeight(pill.frame)) + 6;
		CGFloat width = titleInset + NSWidth(label.frame) + 24 + NSWidth(pill.frame) + endInset;
		TailtrayRow *row = [[TailtrayRow alloc] initWithFrame:NSMakeRect(0, 0, width, height)];
		row.autoresizingMask = NSViewWidthSizable;
		row.item = item;
		label.frameOrigin = NSMakePoint(titleInset, (height - NSHeight(label.frame)) / 2);
		pill.frameOrigin = NSMakePoint(width - endInset - NSWidth(pill.frame), (height - NSHeight(pill.frame)) / 2);
		pill.autoresizingMask = NSViewMinXMargin;
		[row addSubview:label];
		[row addSubview:pill];
		item.view = row;
		// A checked item makes the menu indent every title for a checkmark,
		// even with this one drawn as a view, so the pill carries the state.
		item.state = NSControlStateValueOff;
	};
	// Done before returning, as the tray library's own menu updates are, so a
	// swap for an older menu can never land after a newer menu is built.
	if ([NSThread isMainThread]) {
		swap();
		return;
	}
	dispatch_sync(dispatch_get_main_queue(), swap);
}

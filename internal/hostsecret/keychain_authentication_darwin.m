//go:build vibermate_native_secrets && darwin

#import <CoreFoundation/CoreFoundation.h>
#import <LocalAuthentication/LocalAuthentication.h>
#import <Security/Security.h>

// The packaged daemon has no surface on which an authentication dialog can be
// completed. Apple recommends a non-interactive LAContext instead of the
// deprecated kSecUseAuthenticationUIFail value for modern SecItem calls.
void vibermatePreventAuthenticationUI(CFMutableDictionaryRef query) {
    @autoreleasepool {
        LAContext *context = [[LAContext alloc] init];
        context.interactionNotAllowed = YES;
        CFDictionarySetValue(
            query,
            kSecUseAuthenticationContext,
            (CFTypeRef)context
        );
        [context release];
    }
}

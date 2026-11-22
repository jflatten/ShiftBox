//go:build darwin && cgo

#import <Foundation/Foundation.h>
#import <Security/Security.h>
#import <ServiceManagement/ServiceManagement.h>

#include <stdint.h>
#include <stdlib.h>
#include <string.h>

// One generic password per installation origin belongs to the macOS login
// keychain. The account ID and expiry travel with the scoped credential.
static NSDictionary *helper_query(const char *origin) {
    return @{
        (__bridge id)kSecClass: (__bridge id)kSecClassGenericPassword,
        (__bridge id)kSecAttrService: @"com.shiftbox.local-helper.workspace-ssh",
        (__bridge id)kSecAttrAccount: [NSString stringWithUTF8String:origin],
    };
}

int32_t shiftbox_keychain_read(const char *origin, void **value, size_t *length) {
    @autoreleasepool {
        NSMutableDictionary *query = [NSMutableDictionary dictionaryWithDictionary:helper_query(origin)];
        query[(__bridge id)kSecReturnData] = @YES;
        query[(__bridge id)kSecMatchLimit] = (__bridge id)kSecMatchLimitOne;
        CFTypeRef item = NULL;
        OSStatus status = SecItemCopyMatching((__bridge CFDictionaryRef)query, &item);
        if (status != errSecSuccess) {
            return status;
        }
        if (item == NULL || CFGetTypeID(item) != CFDataGetTypeID()) {
            if (item != NULL) {
                CFRelease(item);
            }
            return errSecDecode;
        }
        CFDataRef data = (CFDataRef)item;
        CFIndex size = CFDataGetLength(data);
        if (size <= 0 || size > 8192) {
            CFRelease(item);
            return errSecDecode;
        }
        void *copy = malloc((size_t)size);
        if (copy == NULL) {
            CFRelease(item);
            return errSecAllocate;
        }
        memcpy(copy, CFDataGetBytePtr(data), (size_t)size);
        CFRelease(item);
        *length = (size_t)size;
        *value = copy;
        return errSecSuccess;
    }
}

int32_t shiftbox_keychain_save(const char *origin, const void *value, size_t length) {
    @autoreleasepool {
        NSDictionary *query = helper_query(origin);
        NSData *data = [NSData dataWithBytes:value length:length];
        NSDictionary *update = @{(__bridge id)kSecValueData: data};
        OSStatus status = SecItemUpdate((__bridge CFDictionaryRef)query, (__bridge CFDictionaryRef)update);
        if (status != errSecItemNotFound) {
            return status;
        }
        NSMutableDictionary *entry = [NSMutableDictionary dictionaryWithDictionary:query];
        [entry addEntriesFromDictionary:update];
        return SecItemAdd((__bridge CFDictionaryRef)entry, NULL);
    }
}

int32_t shiftbox_keychain_remove(const char *origin) {
    @autoreleasepool {
        return SecItemDelete((__bridge CFDictionaryRef)helper_query(origin));
    }
}

int shiftbox_login_item(char **message) {
    @autoreleasepool {
        if (@available(macOS 13.0, *)) {
            SMAppService *service = [SMAppService mainAppService];
            switch (service.status) {
                case SMAppServiceStatusEnabled:
                    return 0;
                case SMAppServiceStatusRequiresApproval:
                    *message = strdup("enable ShiftBox Local Helper in System Settings > General > Login Items");
                    return 1;
                // A first launch can report notFound before it has a system
                // record. Attempt registration and use its actual OS result.
                case SMAppServiceStatusNotFound:
                case SMAppServiceStatusNotRegistered: {
                    NSError *error = nil;
                    if (![service registerAndReturnError:&error]) {
                        if (error.localizedDescription != nil) {
                            *message = strdup(error.localizedDescription.UTF8String);
                        }
                        return 1;
                    }
                    if (service.status == SMAppServiceStatusRequiresApproval) {
                        *message = strdup("enable ShiftBox Local Helper in System Settings > General > Login Items");
                        return 1;
                    }
                    return 0;
                }
            }
        }
        *message = strdup("automatic login startup requires macOS 13 or later");
        return 1;
    }
}

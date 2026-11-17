#pragma once
#include <chrono>
#include <cstdlib>
#include <cstdint>

extern "C" void ND_StreamTrace(char*, unsigned long long, unsigned long long,
                                long long, long long, long long);
extern "C" void ND_StreamTraceName(char*, unsigned long long);

inline bool ndTraceEnabled() {
    static bool enabled = []() {
        const char* value = std::getenv("NANODESK_STREAM_TRACE_DIR");
        return value && *value;
    }();
    return enabled;
}

// Measures metadata only; disabled spans do not read the clock or call Go.
struct NDTraceSpan {
    const char* stage;
    unsigned long long object, frame;
    long long value = 0, result = 0;
    std::chrono::steady_clock::time_point start;

    NDTraceSpan(const char* name, const void* instance, unsigned long long id = 0)
        : stage(name), object(reinterpret_cast<uintptr_t>(instance)), frame(id) {
        if (ndTraceEnabled()) start = std::chrono::steady_clock::now();
    }
    ~NDTraceSpan() {
        if (!ndTraceEnabled()) return;
        auto elapsed = std::chrono::duration_cast<std::chrono::microseconds>(
            std::chrono::steady_clock::now() - start).count();
        ND_StreamTrace(const_cast<char*>(stage), object, frame, elapsed, value, result);
    }
};

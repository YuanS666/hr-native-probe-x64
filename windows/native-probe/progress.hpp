#pragma once
#include <stdexcept>

namespace probe {
// CaptureStall 只标记采集编码无进展超时，坏帧或格式错误仍走永久错误分支。
struct CaptureStall : std::runtime_error { using std::runtime_error::runtime_error; };

// checkProgress 按单调毫秒时间检查整体及最旧输入进度；oldest<0表示没有待编码输入。
inline void checkProgress(double now,double last,double oldest) {
    if(now-last>=5000) throw CaptureStall("no accepted input/output progress for 5s");
    if(oldest>=0 && now-oldest>=5000) throw CaptureStall("oldest encoded frame pending for 5s");
}
}

#include <windows.h>
#include <d3d11.h>
#include <d3d10.h>
#include <dxgi1_6.h>
#include <mfapi.h>
#include <mfidl.h>
#include <mferror.h>
#include <mftransform.h>
#include <codecapi.h>
#include <wmcodecdsp.h>
#include <wrl/client.h>
#include <psapi.h>
#include <pdh.h>
#include <pdhmsg.h>
#include <io.h>
#include <fcntl.h>
#include <algorithm>
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <filesystem>
#include <fstream>
#include <iomanip>
#include <iostream>
#include <map>
#include <sstream>
#include <string>
#include <thread>
#include "bitstream.hpp"
#include "progress.hpp"
#include "page.hpp"

using Microsoft::WRL::ComPtr;
using probe::Bytes;
using probe::require;
namespace fs = std::filesystem;

// nowMS 读取单调 QPC 毫秒时钟，供同进程耗时和帧率测量。
static double nowMS() {
    LARGE_INTEGER counter, frequency;
    QueryPerformanceCounter(&counter); QueryPerformanceFrequency(&frequency);
    return double(counter.QuadPart)*1000/double(frequency.QuadPart);
}

// utf8 将系统的 UTF-16 设备名称转换为 UTF-8 日志，不改变文件路径语义。
static std::string utf8(const wchar_t* text) {
    int n=WideCharToMultiByte(CP_UTF8,0,text,-1,nullptr,0,nullptr,nullptr);
    require(n>0,"UTF-8 conversion failed");
    std::string out(static_cast<std::size_t>(n),0);
    WideCharToMultiByte(CP_UTF8,0,text,-1,out.data(),n,nullptr,nullptr);
    out.pop_back(); return out;
}

struct CaptureTrace {
    const char* phase="init";
    bool frame=false,duplication=false;
    unsigned initialized=0,released=0,acquired=0,returned=0,releaseCalls=0;
    double lastSuccess=0;
    std::string adapter="unknown",output="unknown";
};
static CaptureTrace trace;

// objectName 只查询窗口站或桌面名称，失败时保留Win32错误，不申请控制安全桌面的权限。
static std::string objectName(HANDLE object) {
    wchar_t name[256]{};DWORD needed=0;
    if(!object || !GetUserObjectInformationW(object,UOI_NAME,name,sizeof(name),&needed))
        return "unavailable:"+std::to_string(GetLastError());
    return utf8(name);
}

// diagnostic 记录失败现场及资源计数，区分线程桌面、输入桌面、拓扑和最后一次成功采集时间。
static void diagnostic(const char* api,HRESULT hr) {
    DWORD session=0;ProcessIdToSessionId(GetCurrentProcessId(),&session);
    HDESK input=OpenInputDesktop(0,FALSE,DESKTOP_READOBJECTS);
    DWORD inputError=input?0:GetLastError();
    std::string inputName=input?objectName(input):"denied:"+std::to_string(inputError);
    if(input) CloseDesktop(input);
    std::ostringstream line;
    line<<"DXGI utc_ms="<<std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::system_clock::now().time_since_epoch()).count()<<" phase="<<trace.phase<<" api="<<api<<" hr=0x"<<std::hex<<static_cast<unsigned long>(hr)<<std::dec
        <<" pid="<<GetCurrentProcessId()<<" session="<<session<<" console="<<WTSGetActiveConsoleSessionId()
        <<" tid="<<GetCurrentThreadId()<<" station="<<objectName(GetProcessWindowStation())
        <<" thread_desktop="<<objectName(GetThreadDesktop(GetCurrentThreadId()))<<" input_desktop="<<inputName
        <<" primary="<<GetSystemMetrics(SM_CXSCREEN)<<"x"<<GetSystemMetrics(SM_CYSCREEN)
        <<" monitors="<<GetSystemMetrics(SM_CMONITORS)<<" adapter="<<trace.adapter<<" output="<<trace.output
        <<" holding_dup="<<trace.duplication<<" holding_frame="<<trace.frame
        <<" init="<<trace.initialized<<" cleanup="<<trace.released
        <<" acquired="<<trace.acquired<<" release_calls="<<trace.releaseCalls<<" returned="<<trace.returned
        <<" last_success_age_ms="<<(trace.lastSuccess?nowMS()-trace.lastSuccess:-1);
    std::cerr<<line.str()<<"\n";
}

struct NativeFailure : std::runtime_error {
    bool retry;
    // NativeFailure 保留具体API/HRESULT，只有已分类的桌面或设备切换错误允许Go有限恢复。
    NativeFailure(HRESULT hr,const char* api,bool recover):std::runtime_error(api),retry(recover) {
        diagnostic(api,hr);
    }
};

// checked 对系统错误输出完整现场；原生进程退出后由Go回收并重建整个图形资源链。
static void checked(HRESULT hr,const char* api) {
    if(SUCCEEDED(hr)) return;
    bool retry=hr==DXGI_ERROR_ACCESS_LOST || hr==E_ACCESSDENIED || hr==DXGI_ERROR_DEVICE_REMOVED
        || hr==DXGI_ERROR_DEVICE_RESET || hr==DXGI_ERROR_SESSION_DISCONNECTED;
    throw NativeFailure(hr,api,retry);
}

struct Runtime {
    // Runtime 初始化当前线程 COM 和系统媒体组件，缺少 Media Feature Pack 时明确失败。
    Runtime() {
        checked(CoInitializeEx(nullptr,COINIT_MULTITHREADED),"CoInitializeEx");
        HRESULT hr=MFStartup(MF_VERSION,MFSTARTUP_FULL);
        if(FAILED(hr)) {CoUninitialize();checked(hr,"MFStartup (check Windows Media Feature Pack)");}
    }
    // ~Runtime 在所有媒体对象销毁后关闭系统组件。
    ~Runtime() {MFShutdown();CoUninitialize();}
};

struct Options {
    UINT width=1920,height=1080,fps=20,bitrate=2000000,seconds=10;
    std::wstring encoder=L"auto";
    fs::path output=L"capture";
    bool stream=false;
};

// options 读取有限的探针参数，防止误生成无限时长或极大尺寸文件。
static Options options(int argc,wchar_t** argv) {
    Options o;
    for(int i=1;i<argc;++i) {
        std::wstring key=argv[i];
        if(key==L"--stream") {o.stream=true;continue;}
        if(key==L"--help") {
            std::cout<<"screen_native_probe [--encoder auto|hardware|software] [--seconds 10] [--output capture] [--width 1920] [--height 1080] [--fps 20] [--bitrate 2000000] [--stream (software only)]\n";
            std::exit(0);
        }
        require(i+1<argc,"missing option value");
        std::wstring value=argv[++i];
        if(key==L"--output") o.output=value;
        else if(key==L"--encoder") o.encoder=value;
        else {
            require(!value.empty()&&value.find_first_not_of(L"0123456789")==std::wstring::npos,"option must be a positive integer");
            unsigned long n=std::stoul(value);
            if(key==L"--width") o.width=n;
            else if(key==L"--height") o.height=n;
            else if(key==L"--fps") o.fps=n;
            else if(key==L"--bitrate") o.bitrate=n;
            else if(key==L"--seconds") o.seconds=n;
            else throw std::runtime_error("unknown option");
        }
    }
    require(o.width>=2&&o.width<=3840&&o.width%2==0&&o.height>=2&&o.height<=2160&&o.height%2==0,"output size must be even, <=3840x2160");
    require(o.fps>=1&&o.fps<=60&&o.seconds>=2&&o.seconds<=120&&o.bitrate>=10000&&o.bitrate<=20000000,"fps=1..60, seconds=2..120, bitrate=10000..20000000");
    require(o.encoder==L"auto"||o.encoder==L"hardware"||o.encoder==L"software","invalid encoder mode");
    require(!o.stream||o.encoder==L"software","--stream requires explicit --encoder software; hardware integration is not verified");
    return o;
}

// ordinaryDesktop 拒绝服务、RDP 和安全桌面，只验证已登录物理控制台的普通桌面。
static void ordinaryDesktop() {
    DWORD session=0;
    require(ProcessIdToSessionId(GetCurrentProcessId(),&session)&&session!=0,"run in a logged-in user session, not service");
    DWORD console=WTSGetActiveConsoleSessionId();
    if(console==0xffffffff) throw NativeFailure(DXGI_ERROR_SESSION_DISCONNECTED,"physical console temporarily disconnected",true);
    require(session==console,"run in the logged-in physical console, not another user/RDP");
    require(objectName(GetProcessWindowStation())=="WinSta0","run on interactive WinSta0 only");
    HDESK desktop=OpenInputDesktop(0,FALSE,DESKTOP_READOBJECTS);
    if(!desktop) throw NativeFailure(HRESULT_FROM_WIN32(GetLastError()),"OpenInputDesktop",true);
    std::string name=objectName(desktop);
    CloseDesktop(desktop);
    if(name!="Default") throw NativeFailure(E_ACCESSDENIED,"input desktop is not Default; capture paused",true);
    // 不切换到其他桌面；同名但不同对象的场景通过日志和新的采集进程诊断。
    if(objectName(GetThreadDesktop(GetCurrentThreadId()))!=name)
        throw NativeFailure(E_ACCESSDENIED,"thread/input desktop mismatch",true);
}

struct Capture {
    ComPtr<ID3D11Device> device;
    ComPtr<ID3D11DeviceContext> context;
    ComPtr<IDXGIOutputDuplication> duplication;
    ComPtr<IDXGIOutput1> output;
    ComPtr<ID3D11Texture2D> bgra;
    ComPtr<ID3D11VideoDevice> videoDevice;
    ComPtr<ID3D11VideoContext> videoContext;
    ComPtr<ID3D11VideoProcessorEnumerator> videoEnum;
    ComPtr<ID3D11VideoProcessor> processor;
    ComPtr<ID3D11VideoProcessorInputView> inputView;
    DXGI_ADAPTER_DESC1 adapter{};
    UINT sourceW=0,sourceH=0;
    RECT content{};
    bool haveFrame=false;
    UINT64 fresh=0,repeated=0;

    // Capture 选择真实主屏对应 adapter，初始化物理尺寸和 GPU 色彩转换，不选择任意独显。
    explicit Capture(const Options& o) try {
        trace.phase="init";++trace.initialized;
        ordinaryDesktop();
        require(SetProcessDpiAwarenessContext(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2)!=FALSE,"SetProcessDpiAwarenessContext failed; run without DPI compatibility overrides");
        ComPtr<IDXGIFactory1> factory;
        checked(CreateDXGIFactory1(IID_PPV_ARGS(&factory)),"CreateDXGIFactory1");
        HMONITOR primary=MonitorFromPoint(POINT{0,0},MONITOR_DEFAULTTOPRIMARY);
        ComPtr<IDXGIAdapter1> selected;
        DXGI_OUTPUT_DESC desc{};
        for(UINT a=0;!selected;++a) {
            ComPtr<IDXGIAdapter1> candidate;
            HRESULT hr=factory->EnumAdapters1(a,&candidate);
            if(hr==DXGI_ERROR_NOT_FOUND) break;
            checked(hr,"EnumAdapters1");
            for(UINT n=0;;++n) {
                ComPtr<IDXGIOutput> candidateOutput;
                hr=candidate->EnumOutputs(n,&candidateOutput);
                if(hr==DXGI_ERROR_NOT_FOUND) break;
                checked(hr,"EnumOutputs");
                checked(candidateOutput->GetDesc(&desc),"Get output description");
                if(desc.Monitor==primary&&desc.AttachedToDesktop) {
                    selected=candidate;
                    checked(candidateOutput.As(&output),"IDXGIOutput1");break;
                }
            }
        }
        if(!selected || !output) throw NativeFailure(DXGI_ERROR_NOT_FOUND,"no attached primary display",true);
        require(desc.Rotation==DXGI_MODE_ROTATION_IDENTITY,"rotated primary display is not supported by this probe");
        ComPtr<IDXGIOutput6> modernOutput;
        if(SUCCEEDED(output.As(&modernOutput))) {
            DXGI_OUTPUT_DESC1 color{};
            checked(modernOutput->GetDesc1(&color),"display color space");
            require(color.ColorSpace==DXGI_COLOR_SPACE_RGB_FULL_G22_NONE_P709,"probe supports SDR only; disable HDR for this test");
        }
        checked(selected->GetDesc1(&adapter),"Get adapter description");
        trace.adapter=utf8(adapter.Description)+" LUID="+std::to_string(adapter.AdapterLuid.HighPart)+":"+std::to_string(adapter.AdapterLuid.LowPart);
        trace.output=utf8(desc.DeviceName)+" rect="+std::to_string(desc.DesktopCoordinates.left)+","+std::to_string(desc.DesktopCoordinates.top)+","+std::to_string(desc.DesktopCoordinates.right)+","+std::to_string(desc.DesktopCoordinates.bottom);
        diagnostic("selected adapter/output",S_OK);
        UINT flags=D3D11_CREATE_DEVICE_BGRA_SUPPORT|D3D11_CREATE_DEVICE_VIDEO_SUPPORT;
        D3D_FEATURE_LEVEL level;
        checked(D3D11CreateDevice(selected.Get(),D3D_DRIVER_TYPE_UNKNOWN,nullptr,flags,nullptr,0,D3D11_SDK_VERSION,&device,&level,&context),"D3D11CreateDevice");
        // 异步MFT会从内部线程访问共享D3D11设备，必须保护即时上下文。
        ComPtr<ID3D10Multithread> multithread;
        checked(context.As(&multithread),"ID3D10Multithread");
        multithread->SetMultithreadProtected(TRUE);
        require(multithread->GetMultithreadProtected()!=FALSE,"D3D11 multithread protection unavailable");
        checked(output->DuplicateOutput(device.Get(),&duplication),"DuplicateOutput");
        trace.duplication=true;
        DXGI_OUTDUPL_DESC du{};duplication->GetDesc(&du);
        sourceW=du.ModeDesc.Width;sourceH=du.ModeDesc.Height;
        require(sourceW>=2&&sourceH>=2&&du.ModeDesc.Format==DXGI_FORMAT_B8G8R8A8_UNORM,"probe requires BGRA8 SDR capture");
        D3D11_TEXTURE2D_DESC texture{};
        texture.Width=sourceW;texture.Height=sourceH;texture.MipLevels=1;texture.ArraySize=1;
        texture.Format=DXGI_FORMAT_B8G8R8A8_UNORM;texture.SampleDesc.Count=1;
        texture.Usage=D3D11_USAGE_DEFAULT;texture.BindFlags=D3D11_BIND_RENDER_TARGET;
        checked(device->CreateTexture2D(&texture,nullptr,&bgra),"create owned BGRA texture");
        checked(device.As(&videoDevice),"ID3D11VideoDevice");
        checked(context.As(&videoContext),"ID3D11VideoContext");
        D3D11_VIDEO_PROCESSOR_CONTENT_DESC cd{};
        cd.InputFrameFormat=D3D11_VIDEO_FRAME_FORMAT_PROGRESSIVE;
        cd.InputFrameRate={o.fps,1};cd.OutputFrameRate={o.fps,1};
        cd.InputWidth=sourceW;cd.InputHeight=sourceH;cd.OutputWidth=o.width;cd.OutputHeight=o.height;
        cd.Usage=D3D11_VIDEO_USAGE_PLAYBACK_NORMAL;
        checked(videoDevice->CreateVideoProcessorEnumerator(&cd,&videoEnum),"CreateVideoProcessorEnumerator");
        UINT supported=0;
        checked(videoEnum->CheckVideoProcessorFormat(DXGI_FORMAT_NV12,&supported),"check NV12 conversion");
        require((supported&D3D11_VIDEO_PROCESSOR_FORMAT_SUPPORT_OUTPUT)!=0,"GPU cannot output NV12");
        checked(videoDevice->CreateVideoProcessor(videoEnum.Get(),0,&processor),"CreateVideoProcessor");
        D3D11_VIDEO_PROCESSOR_INPUT_VIEW_DESC iv{};
        iv.ViewDimension=D3D11_VPIV_DIMENSION_TEXTURE2D;
        checked(videoDevice->CreateVideoProcessorInputView(bgra.Get(),videoEnum.Get(),&iv,&inputView),"CreateVideoProcessorInputView");
        UINT w=o.width,h=o.height;
        if(UINT64(sourceW)*h>UINT64(sourceH)*w) h=std::max(2u,UINT(UINT64(sourceH)*w/sourceW)&~1u);
        else w=std::max(2u,UINT(UINT64(sourceW)*h/sourceH)&~1u);
        LONG left=LONG(((o.width-w)/2)&~1u),top=LONG(((o.height-h)/2)&~1u);
        content={left,top,left+LONG(w),top+LONG(h)};
        RECT source{0,0,LONG(sourceW),LONG(sourceH)},target{0,0,LONG(o.width),LONG(o.height)};
        videoContext->VideoProcessorSetStreamFrameFormat(processor.Get(),0,D3D11_VIDEO_FRAME_FORMAT_PROGRESSIVE);
        videoContext->VideoProcessorSetStreamAutoProcessingMode(processor.Get(),0,FALSE);
        videoContext->VideoProcessorSetStreamSourceRect(processor.Get(),0,TRUE,&source);
        videoContext->VideoProcessorSetStreamDestRect(processor.Get(),0,TRUE,&content);
        videoContext->VideoProcessorSetOutputTargetRect(processor.Get(),TRUE,&target);
        D3D11_VIDEO_COLOR black{};black.YCbCr.Y=0.0625f;black.YCbCr.Cb=0.5f;black.YCbCr.Cr=0.5f;black.YCbCr.A=1;
        videoContext->VideoProcessorSetOutputBackgroundColor(processor.Get(),TRUE,&black);
        D3D11_VIDEO_PROCESSOR_COLOR_SPACE rgb{},yuv{};
        rgb.RGB_Range=0;rgb.Nominal_Range=D3D11_VIDEO_PROCESSOR_NOMINAL_RANGE_0_255;
        yuv.YCbCr_Matrix=1;yuv.Nominal_Range=D3D11_VIDEO_PROCESSOR_NOMINAL_RANGE_16_235;
        videoContext->VideoProcessorSetStreamColorSpace(processor.Get(),0,&rgb);
        videoContext->VideoProcessorSetOutputColorSpace(processor.Get(),&yuv);
        std::cerr<<"adapter="<<utf8(adapter.Description)<<" LUID="<<adapter.AdapterLuid.HighPart<<":"<<adapter.AdapterLuid.LowPart
                 <<" primary="<<sourceW<<"x"<<sourceH<<" output="<<o.width<<"x"<<o.height
                 <<" content="<<left<<","<<top<<","<<w<<","<<h<<"\n";
        std::cerr<<"SDR primary only; cursor composition and HDR are not validated. No audio/input/network.\n";
    } catch(...) {
        // 构造失败时成员COM指针已自动析构，此处仅记录已完成的清理，不访问失效成员。
        trace.phase="init-cleanup";trace.duplication=false;trace.frame=false;++trace.released;
        diagnostic("partial initialization released",S_OK);
        throw;
    }

    // releaseFrame 只归还已经取得的帧，异常清理时记录失败但不覆盖最初的故障原因。
    void releaseFrame(bool check) {
        if(!trace.frame) return;
        trace.phase="release";
        HRESULT hr=duplication->ReleaseFrame();
        trace.frame=false;++trace.releaseCalls;
        if(SUCCEEDED(hr)) ++trace.returned;
        if(check) checked(hr,"ReleaseFrame");
        else if(FAILED(hr)) diagnostic("ReleaseFrame during cleanup",hr);
    }

    // ~Capture 依次释放帧、转换视图、纹理、Duplication和设备，进程退出前报告配对计数。
    ~Capture() {
        releaseFrame(false);trace.phase="cleanup";
        inputView.Reset();processor.Reset();videoEnum.Reset();bgra.Reset();
        duplication.Reset();trace.duplication=false;output.Reset();
        videoContext.Reset();videoDevice.Reset();
        if(context) {context->ClearState();context->Flush();}
        context.Reset();device.Reset();++trace.released;
        diagnostic("capture resources released",S_OK);
    }

    // next 超时正常返回；成功Acquire后只Release一次，失效时退出以完整重建而非复用旧设备。
    bool next() {
        trace.phase="capture";
        ordinaryDesktop();
        if(UINT(GetSystemMetrics(SM_CXSCREEN))!=sourceW || UINT(GetSystemMetrics(SM_CYSCREEN))!=sourceH)
            throw NativeFailure(DXGI_ERROR_ACCESS_LOST,"primary resolution changed",true);
        DXGI_OUTPUT_DESC current{};checked(output->GetDesc(&current),"GetDesc before capture");
        if(!current.AttachedToDesktop || current.Monitor!=MonitorFromPoint(POINT{0,0},MONITOR_DEFAULTTOPRIMARY))
            throw NativeFailure(DXGI_ERROR_ACCESS_LOST,"primary output changed",true);
        DXGI_OUTDUPL_FRAME_INFO info{};
        ComPtr<IDXGIResource> resource;
        HRESULT hr=duplication->AcquireNextFrame(haveFrame?0:1000,&info,&resource);
        if(hr==DXGI_ERROR_WAIT_TIMEOUT) {if(haveFrame) ++repeated;return haveFrame;}
        if(FAILED(hr)) {
            HRESULT removed=device->GetDeviceRemovedReason();
            diagnostic("AcquireNextFrame",hr);
            checked(removed,"GetDeviceRemovedReason after AcquireNextFrame");
            checked(hr,"AcquireNextFrame");
        }
        trace.frame=true;++trace.acquired;
        try {
            if(info.LastPresentTime.QuadPart>0||!haveFrame) {
                ComPtr<ID3D11Texture2D> texture;
                checked(resource.As(&texture),"desktop texture");
                D3D11_TEXTURE2D_DESC desc{};texture->GetDesc(&desc);
                if(desc.Width!=sourceW || desc.Height!=sourceH || desc.Format!=DXGI_FORMAT_B8G8R8A8_UNORM)
                    throw NativeFailure(DXGI_ERROR_ACCESS_LOST,"captured texture layout changed",true);
                context->CopyResource(bgra.Get(),texture.Get());
                checked(device->GetDeviceRemovedReason(),"GetDeviceRemovedReason after CopyResource");
                haveFrame=true;++fresh;
            } else ++repeated;
            trace.lastSuccess=nowMS();
        } catch(...) {resource.Reset();releaseFrame(false);throw;}
        resource.Reset();releaseFrame(true);
        trace.phase="convert";
        return true;
    }

    // nv12 为本次输入创建独立 NV12 纹理，避免异步 MFT 尚在读取时被下一帧覆盖。
    ComPtr<ID3D11Texture2D> nv12(const Options& o) {
        D3D11_TEXTURE2D_DESC desc{};
        desc.Width=o.width;desc.Height=o.height;desc.MipLevels=1;desc.ArraySize=1;
        desc.Format=DXGI_FORMAT_NV12;desc.SampleDesc.Count=1;
        desc.Usage=D3D11_USAGE_DEFAULT;desc.BindFlags=D3D11_BIND_RENDER_TARGET;
        ComPtr<ID3D11Texture2D> texture;
        checked(device->CreateTexture2D(&desc,nullptr,&texture),"create NV12 input");
        D3D11_VIDEO_PROCESSOR_OUTPUT_VIEW_DESC ov{};ov.ViewDimension=D3D11_VPOV_DIMENSION_TEXTURE2D;
        ComPtr<ID3D11VideoProcessorOutputView> view;
        checked(videoDevice->CreateVideoProcessorOutputView(texture.Get(),videoEnum.Get(),&ov,&view),"CreateVideoProcessorOutputView");
        D3D11_VIDEO_PROCESSOR_STREAM stream{};stream.Enable=TRUE;stream.pInputSurface=inputView.Get();
        checked(videoContext->VideoProcessorBlt(processor.Get(),view.Get(),0,1,&stream),"VideoProcessorBlt");
        return texture;
    }

    // sample 为硬编封装 GPU 表面，软件路径按 RowPitch 读取 NV12；两条路径都保留100ns时间戳。
    ComPtr<IMFSample> sample(const Options& o,ID3D11Texture2D* texture,bool hardware,LONGLONG pts,LONGLONG duration) {
        ComPtr<IMFMediaBuffer> buffer;
        if(hardware) {
            checked(MFCreateDXGISurfaceBuffer(__uuidof(ID3D11Texture2D),texture,0,FALSE,&buffer),"MFCreateDXGISurfaceBuffer");
            ComPtr<IMF2DBuffer> image;
            checked(buffer.As(&image),"IMF2DBuffer");
            DWORD length=0;checked(image->GetContiguousLength(&length),"GPU surface length");
            checked(buffer->SetCurrentLength(length),"GPU surface current length");
        } else {
            D3D11_TEXTURE2D_DESC desc{};texture->GetDesc(&desc);
            desc.Usage=D3D11_USAGE_STAGING;desc.BindFlags=0;desc.CPUAccessFlags=D3D11_CPU_ACCESS_READ;
            ComPtr<ID3D11Texture2D> staging;
            checked(device->CreateTexture2D(&desc,nullptr,&staging),"create NV12 readback");
            context->CopyResource(staging.Get(),texture);
            checked(MFCreateMemoryBuffer(o.width*o.height*3/2,&buffer),"MFCreateMemoryBuffer");
            BYTE* destination=nullptr;
            checked(buffer->Lock(&destination,nullptr,nullptr),"lock NV12 buffer");
            D3D11_MAPPED_SUBRESOURCE map{};
            HRESULT hr=context->Map(staging.Get(),0,D3D11_MAP_READ,0,&map);
            if(FAILED(hr)) {
                buffer->Unlock();diagnostic("map NV12 readback",hr);
                checked(device->GetDeviceRemovedReason(),"GetDeviceRemovedReason after Map");
                checked(hr,"map NV12 readback");
            }
            for(UINT row=0;row<o.height*3/2;++row)
                std::memcpy(destination+std::size_t(row)*o.width,static_cast<BYTE*>(map.pData)+std::size_t(row)*map.RowPitch,o.width);
            context->Unmap(staging.Get(),0);
            checked(buffer->Unlock(),"unlock NV12 buffer");
            checked(buffer->SetCurrentLength(o.width*o.height*3/2),"NV12 length");
        }
        ComPtr<IMFSample> result;
        checked(MFCreateSample(&result),"MFCreateSample");
        checked(result->AddBuffer(buffer.Get()),"sample AddBuffer");
        checked(result->SetSampleTime(pts),"SetSampleTime");
        checked(result->SetSampleDuration(duration),"SetSampleDuration");
        return result;
    }
};

// mediaType 创建明确的 NV12 或 H264 媒体类型，固定SDR BT.709、方形像素和逐行画面。
static ComPtr<IMFMediaType> mediaType(const Options& o,bool output) {
    ComPtr<IMFMediaType> type;
    checked(MFCreateMediaType(&type),"MFCreateMediaType");
    checked(type->SetGUID(MF_MT_MAJOR_TYPE,MFMediaType_Video),"media major type");
    checked(type->SetGUID(MF_MT_SUBTYPE,output?MFVideoFormat_H264:MFVideoFormat_NV12),"media subtype");
    checked(MFSetAttributeSize(type.Get(),MF_MT_FRAME_SIZE,o.width,o.height),"media size");
    checked(MFSetAttributeRatio(type.Get(),MF_MT_FRAME_RATE,o.fps,1),"media fps");
    checked(MFSetAttributeRatio(type.Get(),MF_MT_PIXEL_ASPECT_RATIO,1,1),"pixel aspect");
    checked(type->SetUINT32(MF_MT_INTERLACE_MODE,MFVideoInterlace_Progressive),"progressive");
    checked(type->SetUINT32(MF_MT_VIDEO_PRIMARIES,MFVideoPrimaries_BT709),"primaries");
    checked(type->SetUINT32(MF_MT_TRANSFER_FUNCTION,MFVideoTransFunc_709),"transfer");
    checked(type->SetUINT32(MF_MT_YUV_MATRIX,MFVideoTransferMatrix_BT709),"matrix");
    checked(type->SetUINT32(MF_MT_VIDEO_NOMINAL_RANGE,MFNominalRange_16_235),"range");
    if(output) {
        checked(type->SetUINT32(MF_MT_AVG_BITRATE,o.bitrate),"bitrate");
        checked(type->SetUINT32(MF_MT_MPEG2_PROFILE,eAVEncH264VProfile_Base),"H264 baseline");
    } else {
        checked(type->SetUINT32(MF_MT_DEFAULT_STRIDE,o.width),"NV12 stride");
        checked(type->SetUINT32(MF_MT_SAMPLE_SIZE,o.width*o.height*3/2),"NV12 sample size");
    }
    return type;
}

struct Files {
    std::ofstream raw,page,timing,metrics;
    probe::H264 h264;
    UINT64 frames=0,bytes=0,keys=0;
    LONGLONG lastPTS=-1;
    double maxEncode=0,sumEncode=0;
    std::vector<double> encodeTimes;
    bool streaming=false;

    // Files 在探针模式创建结果文件；Agent模式只切换二进制管道，不生成任何文件。
    explicit Files(const Options& o) {
        streaming=o.stream;
        if(streaming) {
            require(_setmode(_fileno(stdout),_O_BINARY)!=-1,"cannot switch stdout to binary mode");
            std::cerr<<"MF streaming: length-prefixed H264 packets on stdout; no files, no hardware encoding, no FFmpeg.\n";
            return;
        }
        fs::path paths[4]={o.output.wstring()+L".h264",o.output.wstring()+L".html",o.output.wstring()+L"-timing.csv",o.output.wstring()+L"-stats.csv"};
        for(const auto& p:paths) require(!fs::exists(p),"output exists; use another --output prefix");
        raw.open(paths[0],std::ios::binary);page.open(paths[1],std::ios::binary);
        timing.open(paths[2]);metrics.open(paths[3]);
        require(raw.good()&&page.good()&&timing.good()&&metrics.good(),"cannot create output files");
        std::string html=probePage;
        auto end=html.rfind("</html>");require(end!=std::string::npos,"embedded browser page invalid");
        page<<html.substr(0,end)<<"\n<script>\nconst nativeFrames=[\n";
        timing<<"frame,pts_100ns,key,bytes,submit_to_output_ms\n";
        metrics<<"elapsed_s,new_desktop_frames,repeated_frames,submitted,encoded,fps,mbps,cpu_percent_of_machine,working_set_bytes,private_bytes,gpu_encode_max_percent,gpu_processing_max_percent,gpu_copy_max_percent,gpu_3d_max_percent\n";
    }

    // frame 校验单帧和时间戳，保存原Annex B及现有17字节协议头的离线副本。
    bool frame(const Bytes& data,LONGLONG pts,double latencyMS) {
        auto au=h264.normalize(data);
        if(au.data.empty()) return false;
        require(pts>=0&&pts>lastPTS,"non-increasing or missing output PTS");
        require(frames!=0||au.key,"first encoded frame is not IDR");
        auto packet=probe::videoPacket(au,UINT64(pts));
        if(streaming) {
            auto record=probe::streamRecord(packet);
            // 同步管道反压使慢浏览器不能积压无限视频；Go断连时会终止此进程。
            require(std::fwrite(record.data(),1,record.size(),stdout)==record.size()&&std::fflush(stdout)==0,"native stream pipe closed or write failed");
        } else {
            require(bytes+au.data.size()<=64u*1024u*1024u,"probe output exceeded 64 MiB limit");
            raw.write(reinterpret_cast<const char*>(au.data.data()),static_cast<std::streamsize>(au.data.size()));
            if(frames) page<<",\n";
            page<<'"'<<probe::base64(packet)<<'"';
            timing<<frames<<','<<pts<<','<<au.key<<','<<au.data.size()<<','<<latencyMS<<'\n';
            require(raw.good()&&page.good()&&timing.good(),"output write failed");
        }
        ++frames;keys+=au.key;bytes+=au.data.size();lastPTS=pts;
        maxEncode=std::max(maxEncode,latencyMS);sumEncode+=latencyMS;
        // 实时运行不保存逐帧历史，避免长时间运行时统计数组持续增长。
        if(!streaming) encodeTimes.push_back(latencyMS);
        return true;
    }

    // finish 仅在完整 drain 成功后完成离线网页，失败残留文件不伪装成可验收输出。
    void finish(const Options& o) {
        require(h264.sps.size()>=4&&frames>0,"no encoded frames");
        char codec[32];std::snprintf(codec,sizeof(codec),"avc1.%02x%02x%02x",h264.sps[1],h264.sps[2],h264.sps[3]);
        page<<"\n];\nconst nativeInfo={codec:'"<<codec<<"',width:"<<o.width<<",height:"<<o.height<<",fps:"<<o.fps<<"};\n"<<probeScript<<"\n</script></html>\n";
        raw.flush();page.flush();timing.flush();metrics.flush();
        require(raw.good()&&page.good()&&timing.good()&&metrics.good(),"final output flush failed");
        std::sort(encodeTimes.begin(),encodeTimes.end());
        auto p95=encodeTimes[(encodeTimes.size()*95+99)/100-1];
        std::cerr<<"COMPLETE: encoded="<<frames<<" IDR="<<keys<<" bytes="<<bytes<<" submit-to-output mean/P95/max="<<sumEncode/frames<<"/"<<p95<<"/"<<maxEncode<<" ms (includes queueing, NOT pure GPU encode time)\n";
        std::cerr<<"WebCodecs, real-time transport and glass-to-glass latency: NOT VERIFIED; open the generated HTML in Chrome.\n";
    }
};

struct Encoder {
    ComPtr<IMFTransform> transform;
    ComPtr<IMFMediaEventGenerator> events;
    ComPtr<ICodecAPI> codec;
    ComPtr<IMFDXGIDeviceManager> manager;
    bool hardware=false,drained=false;
    unsigned inputTokens=0;
    UINT64 outputCalls=0,outputEvents=0;
    DWORD inputID=0,outputID=0;
    std::map<LONGLONG,double> pending;
    Files& files;
    const Options& opt;

    // Encoder 绑定输出文件和配置；探测失败时不在对象外泄漏媒体会话。
    Encoder(Files& f,const Options& o):files(f),opt(o) {}
    // ~Encoder 终止仍存活的异步 MFT，释放系统硬件资源而不无限等待。
    ~Encoder() {
        if(transform) {transform->ProcessMessage(MFT_MESSAGE_COMMAND_FLUSH,0);MFShutdownObject(transform.Get());}
    }

    // property 设置并记录实际编码属性，关键属性拒绝时让候选失败而不是静默继续。
    void property(const GUID& key,ULONG value,const char* name,bool mandatory=true,bool boolean=false) {
        VARIANT v;VariantInit(&v);v.vt=boolean?VT_BOOL:VT_UI4;
        if(boolean) v.boolVal=value?VARIANT_TRUE:VARIANT_FALSE;else v.ulVal=value;
        HRESULT hr=codec->SetValue(&key,&v);
        std::cerr<<name<<"="<<value<<" HRESULT=0x"<<std::hex<<static_cast<unsigned long>(hr)<<std::dec<<'\n';
        if(mandatory) checked(hr,name);
    }

    // configure 协商输入输出、D3D管理器和流ID，软编和硬编共享H264输出契约。
    void configure(Capture& capture,bool hw) {
        hardware=hw;inputTokens=0;outputCalls=outputEvents=0;
        events.Reset();codec.Reset();manager.Reset();pending.clear();
        ComPtr<IMFAttributes> attrs;
        checked(transform->GetAttributes(&attrs),"MFT attributes");
        UINT32 async=0;attrs->GetUINT32(MF_TRANSFORM_ASYNC,&async);
        if(hw) {
            require(async!=0,"hardware MFT must expose async events");
            checked(attrs->SetUINT32(MF_TRANSFORM_ASYNC_UNLOCK,TRUE),"unlock async MFT");
            UINT32 aware=0;checked(attrs->GetUINT32(MF_SA_D3D11_AWARE,&aware),"MF_SA_D3D11_AWARE");
            require(aware!=0,"MFT does not accept D3D11 surfaces");
            checked(transform.As(&events),"IMFMediaEventGenerator");
            UINT token=0;checked(MFCreateDXGIDeviceManager(&token,&manager),"MFCreateDXGIDeviceManager");
            checked(manager->ResetDevice(capture.device.Get(),token),"ResetDevice");
            checked(transform->ProcessMessage(MFT_MESSAGE_SET_D3D_MANAGER,reinterpret_cast<ULONG_PTR>(manager.Get())),"MFT D3D manager");
        } else require(async==0,"software probe expects synchronous Microsoft MFT");
        checked(transform.As(&codec),"ICodecAPI");
        property(CODECAPI_AVEncCommonRateControlMode,eAVEncCommonRateControlMode_CBR,"CBR");
        property(CODECAPI_AVEncCommonMeanBitRate,opt.bitrate,"mean bitrate");
        property(CODECAPI_AVEncMPVDefaultBPictureCount,0,"B frames");
        property(CODECAPI_AVEncMPVGOPSize,opt.fps,"GOP",false);
        property(CODECAPI_AVLowLatencyMode,1,"low latency",true,true);
        DWORD inputs=0,outputs=0;checked(transform->GetStreamCount(&inputs,&outputs),"GetStreamCount");
        require(inputs==1&&outputs==1,"probe requires one input and one output");
        HRESULT ids=transform->GetStreamIDs(1,&inputID,1,&outputID);
        if(ids==E_NOTIMPL) inputID=outputID=0;else checked(ids,"GetStreamIDs");
        auto out=mediaType(opt,true),in=mediaType(opt,false);
        checked(transform->SetOutputType(outputID,out.Get(),0),"SetOutputType H264 baseline");
        checked(transform->SetInputType(inputID,in.Get(),0),"SetInputType NV12");
        MFT_OUTPUT_STREAM_INFO info{};
        checked(transform->GetOutputStreamInfo(outputID,&info),"negotiated output stream info");
        std::cerr<<"MFT output flags=0x"<<std::hex<<info.dwFlags<<std::dec
                 <<" minimum_buffer="<<info.cbSize<<" alignment="<<info.cbAlignment<<'\n';
        // WHOLE_SAMPLES未声明不能直接判定H264失败，实际AU仍经NAL、切片头、PTS和浏览器校验。
        if(!(info.dwFlags&MFT_OUTPUT_STREAM_WHOLE_SAMPLES))
            std::cerr<<"WHOLE_SAMPLES not advertised; validating actual H264 access units and PTS.\n";
    }

    // choose 只在初始化阶段按同adapter枚举硬编；auto可回退系统软件编码，开始输出后不混用编码器。
    void choose(Capture& capture) {
        if(opt.encoder!=L"software") {
            try {
                ComPtr<IMFAttributes> attrs;checked(MFCreateAttributes(&attrs,1),"enumeration attributes");
                checked(attrs->SetBlob(MFT_ENUM_ADAPTER_LUID,reinterpret_cast<BYTE*>(&capture.adapter.AdapterLuid),sizeof(LUID)),"MFT adapter LUID");
                MFT_REGISTER_TYPE_INFO in{MFMediaType_Video,MFVideoFormat_NV12},out{MFMediaType_Video,MFVideoFormat_H264};
                IMFActivate** list=nullptr;UINT32 count=0;
                // 动态查找系统函数，旧版Win10缺MFTEnum2时auto仍可尝试软件编码。
                using Enum2=HRESULT(WINAPI*)(GUID,UINT32,const MFT_REGISTER_TYPE_INFO*,const MFT_REGISTER_TYPE_INFO*,IMFAttributes*,IMFActivate***,UINT32*);
                HMODULE mfplat=GetModuleHandleW(L"mfplat.dll");
                auto enumerate=reinterpret_cast<Enum2>(GetProcAddress(mfplat,"MFTEnum2"));
                require(enumerate!=nullptr,"MFTEnum2 unavailable; hardware probe requires Windows 10 1703+");
                checked(enumerate(MFT_CATEGORY_VIDEO_ENCODER,MFT_ENUM_FLAG_HARDWARE|MFT_ENUM_FLAG_SORTANDFILTER,&in,&out,attrs.Get(),&list,&count),"MFTEnum2 H264");
                bool chosen=false;
                for(UINT32 i=0;i<count;++i) {
                    if(!chosen) {
                        wchar_t name[256]{};UINT32 length=0;
                        list[i]->GetString(MFT_FRIENDLY_NAME_Attribute,name,256,&length);
                        std::cerr<<"trying hardware MFT: "<<utf8(name)<<'\n';
                        try {
                            GUID clsid{};wchar_t id[64]{};
                            checked(list[i]->GetGUID(MFT_TRANSFORM_CLSID_Attribute,&clsid),"MFT CLSID");
                            StringFromGUID2(clsid,id,64);std::cerr<<"MFT CLSID="<<utf8(id)<<'\n';
                            checked(list[i]->ActivateObject(IID_PPV_ARGS(&transform)),"Activate hardware MFT");
                            configure(capture,true);chosen=true;
                            std::cerr<<"selected hardware MFT: "<<utf8(name)<<'\n';
                        } catch(const std::exception& e) {
                            std::cerr<<"hardware candidate rejected: "<<e.what()<<'\n';
                            list[i]->ShutdownObject();transform.Reset();
                        }
                    }
                    list[i]->Release();
                }
                CoTaskMemFree(list);
                require(chosen,"no usable same-adapter D3D11 H264 hardware MFT");
            } catch(const std::exception& e) {
                if(opt.encoder==L"hardware") throw;
                std::cerr<<"auto fallback to system software MFT: "<<e.what()<<'\n';
            }
        }
        if(!transform) {
            checked(CoCreateInstance(CLSID_CMSH264EncoderMFT,nullptr,CLSCTX_INPROC_SERVER,IID_PPV_ARGS(&transform)),"Microsoft H264 encoder (check Media Feature Pack)");
            configure(capture,false);
            wchar_t id[64]{};StringFromGUID2(CLSID_CMSH264EncoderMFT,id,64);
            std::cerr<<"MFT CLSID="<<utf8(id)<<'\n';
            std::cerr<<"selected Microsoft H264 software MFT; GPU conversion + CPU readback, no FFmpeg\n";
        }
        checked(transform->ProcessMessage(MFT_MESSAGE_NOTIFY_BEGIN_STREAMING,0),"begin streaming");
        checked(transform->ProcessMessage(MFT_MESSAGE_NOTIFY_START_OF_STREAM,0),"start of stream");
    }

    // headers 缓存协商后的Annex B参数集；缺失时允许首个输出sample自带参数集。
    void headers() {
        ComPtr<IMFMediaType> type;checked(transform->GetOutputCurrentType(outputID,&type),"current output type");
        UINT32 size=0;
        if(SUCCEEDED(type->GetBlobSize(MF_MT_MPEG_SEQUENCE_HEADER,&size))&&size) {
            require(size<=65536,"sequence header too large");Bytes data(size);
            checked(type->GetBlob(MF_MT_MPEG_SEQUENCE_HEADER,data.data(),size,&size),"sequence header");
            files.h264.parameters(data);
        }
    }

    // output 读取一个实际编码sample，处理MFT分配/调用者分配与类型变更，拒绝无匹配PTS的帧。
    HRESULT output() {
        MFT_OUTPUT_STREAM_INFO info{};checked(transform->GetOutputStreamInfo(outputID,&info),"output stream info");
        MFT_OUTPUT_DATA_BUFFER data{};data.dwStreamID=outputID;
        ComPtr<IMFSample> supplied;
        if(!(info.dwFlags&MFT_OUTPUT_STREAM_PROVIDES_SAMPLES)) {
            checked(MFCreateSample(&supplied),"output sample");
            ComPtr<IMFMediaBuffer> buffer;
            DWORD capacity=std::max<DWORD>(info.cbSize,opt.width*opt.height*3/2);
            require(capacity<=32u*1024u*1024u,"output sample too large");
            if(info.cbAlignment) {
                require((info.cbAlignment&(info.cbAlignment-1))==0,"MFT output alignment must be power of two");
                checked(MFCreateAlignedMemoryBuffer(capacity,info.cbAlignment-1,&buffer),"aligned output buffer");
            }
            else checked(MFCreateMemoryBuffer(capacity,&buffer),"output buffer");
            checked(supplied->AddBuffer(buffer.Get()),"output AddBuffer");data.pSample=supplied.Get();
        }
        DWORD status=0;HRESULT hr=transform->ProcessOutput(0,1,&data,&status);
        ++outputCalls;
        if(hardware&&(outputCalls<=3||FAILED(hr))) {
            std::cerr<<"MFT ProcessOutput #"<<outputCalls<<" HaveOutput events="<<outputEvents
                     <<" HRESULT=0x"<<std::hex<<static_cast<unsigned long>(hr)
                     <<" stream_status=0x"<<data.dwStatus<<" process_status=0x"<<status<<std::dec
                     <<" sample="<<(data.pSample?"yes":"no")<<" pending="<<pending.size()<<'\n';
        }
        if(data.pEvents) data.pEvents->Release();
        ComPtr<IMFSample> sample;
        if(data.pSample) {if(data.pSample==supplied.Get()) sample=supplied;else sample.Attach(data.pSample);}
        if(hr==MF_E_TRANSFORM_STREAM_CHANGE) {
            // 只接受原尺寸/格式的重新协商，不静默改动浏览器配置。
            std::cerr<<"MFT output type change; renegotiating, then waiting for the next HaveOutput event.\n";
            ComPtr<IMFMediaType> type;
            checked(transform->GetOutputAvailableType(outputID,0,&type),"changed output type");
            UINT32 w=0,h=0;GUID subtype{};
            checked(MFGetAttributeSize(type.Get(),MF_MT_FRAME_SIZE,&w,&h),"changed dimensions");
            checked(type->GetGUID(MF_MT_SUBTYPE,&subtype),"changed subtype");
            require(w==opt.width&&h==opt.height&&subtype==MFVideoFormat_H264,"unsupported output format change");
            checked(transform->SetOutputType(outputID,type.Get(),0),"accept output change");headers();return S_FALSE;
        }
        if(hr==MF_E_TRANSFORM_NEED_MORE_INPUT) return hr;
        checked(hr,"ProcessOutput");
        if(!sample) return S_FALSE;
        ComPtr<IMFMediaBuffer> buffer;checked(sample->ConvertToContiguousBuffer(&buffer),"contiguous H264 output");
        BYTE* p=nullptr;DWORD length=0;checked(buffer->Lock(&p,nullptr,&length),"lock H264 output");
        Bytes bytes;
        try {require(length>0&&length<=16u*1024u*1024u,"H264 sample size invalid");bytes.assign(p,p+length);}
        catch(...) {buffer->Unlock();throw;}
        checked(buffer->Unlock(),"unlock H264 output");
        headers();
        // 参数集单独输出不消费输入帧；VCL存在时才要求能匹配时间戳。
        bool vcl=false;for(const auto& n:probe::splitAnnexB(bytes)) vcl|=(n[0]&31)>=1&&(n[0]&31)<=5;
        if(!vcl) {files.h264.parameters(bytes);return S_OK;}
        LONGLONG pts=0;checked(sample->GetSampleTime(&pts),"encoded PTS");
        auto entry=pending.find(pts);require(entry!=pending.end(),"output PTS not found among submitted frames");
        if(files.frame(bytes,pts,nowMS()-entry->second)) pending.erase(entry);
        return S_OK;
    }

    // pump 按异步事件消费可用输出与输入额度；同步软件编码器只读到 NEED_MORE_INPUT。
    void pump() {
        if(!hardware) {
            for(unsigned n=0;n<32;++n) {if(output()==MF_E_TRANSFORM_NEED_MORE_INPUT) return;}
            throw std::runtime_error("synchronous MFT output did not settle");
        }
        for(unsigned n=0;n<128;++n) {
            ComPtr<IMFMediaEvent> event;
            HRESULT hr=events->GetEvent(MF_EVENT_FLAG_NO_WAIT,&event);
            if(hr==MF_E_NO_EVENTS_AVAILABLE) return;
            checked(hr,"MFT GetEvent");HRESULT status=S_OK;checked(event->GetStatus(&status),"MFT event status");checked(status,"MFT asynchronous error");
            MediaEventType type;checked(event->GetType(&type),"MFT event type");
            if(type==METransformNeedInput) {require(++inputTokens<=32,"MFT input tokens overflow");}
            else if(type==METransformHaveOutput) {
                ++outputEvents;
                // 一次HaveOutput只读一次；类型变更或仅事件输出后也必须等待下一事件，不能同步重读。
                output();
            } else if(type==METransformDrainComplete) drained=true;
            else if(type==MEError) throw std::runtime_error("MFT MEError");
        }
        throw std::runtime_error("MFT event flood");
    }

    // ready 在有限的未完成帧和MFT输入额度内采集，避免慢编码器造成无限队列。
    bool ready() const {return pending.size()<8&&(!hardware||inputTokens>0);}

    // submit 记录同一PTS的提交时间并请求周期关键帧，只有MFT接受输入后才消费额度。
    void submit(IMFSample* sample,LONGLONG pts,bool key) {
        if(key) property(CODECAPI_AVEncVideoForceKeyFrame,1,"force keyframe");
        double submitted=nowMS();
        checked(transform->ProcessInput(inputID,sample,0),"ProcessInput");
        require(pending.emplace(pts,submitted).second,"duplicate input PTS");
        if(hardware) --inputTokens;
    }

    // drain 在有限时间内取完全部输入帧，未输出完、事件缺失或多帧丢失均报失败。
    void drain() {
        checked(transform->ProcessMessage(MFT_MESSAGE_NOTIFY_END_OF_STREAM,inputID),"end of stream");
        checked(transform->ProcessMessage(MFT_MESSAGE_COMMAND_DRAIN,0),"drain");
        double began=nowMS();
        do {
            pump();
            if(!hardware&&pending.empty()) break;
            require(nowMS()-began<10000,"MFT drain exceeded 10s");
            if(hardware&&!drained) Sleep(1);
        } while(hardware&&!drained);
        require(pending.empty(),"MFT drain lost submitted frames");
        checked(transform->ProcessMessage(MFT_MESSAGE_NOTIFY_END_STREAMING,0),"end streaming");
    }
};

struct Meter {
    PDH_HQUERY query=nullptr;PDH_HCOUNTER gpu=nullptr;
    double previousMS=0;
    UINT64 previousCPU=0,previousFrames=0,previousBytes=0;
    DWORD logical=GetActiveProcessorCount(ALL_PROCESSOR_GROUPS);

    // cpuTime 返回当前进程累计内核与用户CPU时间，单位100ns。
    static UINT64 cpuTime() {
        FILETIME a,b,k,u;require(GetProcessTimes(GetCurrentProcess(),&a,&b,&k,&u)!=FALSE,"GetProcessTimes failed");
        return (UINT64(k.dwHighDateTime)<<32|k.dwLowDateTime)+(UINT64(u.dwHighDateTime)<<32|u.dwLowDateTime);
    }
    // Meter 初始化英文GPU计数器路径，系统不支持或无权限时留空而不伪造零占用。
    Meter() {
        previousMS=nowMS();previousCPU=cpuTime();
        if(PdhOpenQueryW(nullptr,0,&query)!=ERROR_SUCCESS||PdhAddEnglishCounterW(query,L"\\GPU Engine(*)\\Utilization Percentage",0,&gpu)!=ERROR_SUCCESS||PdhCollectQueryData(query)!=ERROR_SUCCESS) {
            if(query) PdhCloseQuery(query);query=nullptr;gpu=nullptr;
            std::cerr<<"GPU per-process engine counters unavailable; GPU CSV fields will be empty, not zero.\n";
        }
    }
    // ~Meter 关闭诊断计数器，不改变任何系统性能设置。
    ~Meter() {if(query) PdhCloseQuery(query);}

    // write 按实际经过时间计算CPU/帧率/负载码率，并采集当前进程内存和各类GPU引擎最大占用。
    void write(Files& files,const Capture& capture,UINT64 submitted,double began) {
        double time=nowMS(),elapsed=(time-previousMS)/1000;
        if(elapsed<0.0001) return;
        UINT64 cpu=cpuTime();
        double cpuPercent=double(cpu-previousCPU)/10000000/elapsed/std::max<DWORD>(1,logical)*100;
        double fps=double(files.frames-previousFrames)/elapsed,mbps=double(files.bytes-previousBytes)*8/elapsed/1000000;
        PROCESS_MEMORY_COUNTERS_EX memory{};memory.cb=sizeof(memory);
        require(GetProcessMemoryInfo(GetCurrentProcess(),reinterpret_cast<PROCESS_MEMORY_COUNTERS*>(&memory),sizeof(memory))!=FALSE,"GetProcessMemoryInfo failed");
        double values[4]={-1,-1,-1,-1};
        if(query&&PdhCollectQueryData(query)==ERROR_SUCCESS) {
            DWORD size=0,count=0;
            if(PdhGetFormattedCounterArrayW(gpu,PDH_FMT_DOUBLE,&size,&count,nullptr)==PDH_MORE_DATA&&size<=16u*1024u*1024u) {
                std::vector<BYTE> bytes(size);
                auto items=reinterpret_cast<PDH_FMT_COUNTERVALUE_ITEM_W*>(bytes.data());
                if(PdhGetFormattedCounterArrayW(gpu,PDH_FMT_DOUBLE,&size,&count,items)==ERROR_SUCCESS) {
                    std::wstring pid=L"pid_"+std::to_wstring(GetCurrentProcessId())+L"_";
                    for(DWORD i=0;i<count;++i) {
                        std::wstring name=items[i].szName;
                        if(name.find(pid)==std::wstring::npos||(items[i].FmtValue.CStatus!=PDH_CSTATUS_VALID_DATA&&items[i].FmtValue.CStatus!=PDH_CSTATUS_NEW_DATA)) continue;
                        const wchar_t* names[4]={L"engtype_VideoEncode",L"engtype_VideoProcessing",L"engtype_Copy",L"engtype_3D"};
                        for(unsigned e=0;e<4;++e) if(name.find(names[e])!=std::wstring::npos) values[e]=std::max(values[e],items[i].FmtValue.doubleValue);
                    }
                }
            }
        }
        if(!files.streaming) {
            files.metrics<<(time-began)/1000<<','<<capture.fresh<<','<<capture.repeated<<','<<submitted<<','<<files.frames<<','<<fps<<','<<mbps<<','<<cpuPercent<<','<<memory.WorkingSetSize<<','<<memory.PrivateUsage;
            for(double value:values) {files.metrics<<',';if(value>=0) files.metrics<<value;}
            files.metrics<<'\n';
        }
        std::cerr<<std::fixed<<std::setprecision(2)<<"output="<<fps<<" FPS "<<mbps<<" Mbps CPU="<<cpuPercent<<"% of machine pending="<<submitted-files.frames<<" working="<<memory.WorkingSetSize/1048576.0<<" MiB\n";
        previousMS=time;previousCPU=cpu;previousFrames=files.frames;previousBytes=files.bytes;
    }
};

// wmain 在探针模式执行有界采集，在Agent模式按管道反压持续编码；异常退出且不自行重启。
int wmain(int argc,wchar_t** argv) {
    try {
        Options opt=options(argc,argv);
        std::cerr<<"native-capture revision=7 (retryable encoder stalls; diagnosed capture exits)\n";
        Runtime runtime;
        Capture capture(opt);
        Files files(opt);
        Encoder encoder(files,opt);encoder.choose(capture);
        Meter meter;
        UINT64 submitted=0;
        LONGLONG duration=10000000/opt.fps,nextKey=0,lastPTS=-duration;
        double began=nowMS(),nextTick=began,lastStats=began,lastProgress=began;
        while(opt.stream||(submitted<UINT64(opt.seconds)*opt.fps&&nowMS()-began<double(opt.seconds)*1000)) {
            trace.phase="encode";
            encoder.pump();
            double time=nowMS();
            if(time-lastStats>=1000) {meter.write(files,capture,submitted,began);lastStats=time;}
            probe::checkProgress(time,lastProgress,encoder.pending.empty()?-1:encoder.pending.begin()->second);
            if(!encoder.ready()||time<nextTick) {Sleep(1);continue;}
            if(!capture.next()) {lastProgress=nowMS();continue;}
            auto texture=capture.nv12(opt);
            // 正常固定节奏；编码过慢时保留实际时间缺口，不高速补发旧画面。
            LONGLONG pts=submitted==0?0:std::max(lastPTS+duration,LONGLONG((nowMS()-began)*10000/duration)*duration);
            auto sample=capture.sample(opt,texture.Get(),encoder.hardware,pts,duration);
            bool key=pts>=nextKey;
            trace.phase="encode";
            encoder.submit(sample.Get(),pts,key);
            if(key) nextKey=pts+10000000;
            ++submitted;lastPTS=pts;lastProgress=nowMS();nextTick=std::max(nextTick+1000.0/opt.fps,lastProgress);
        }
        encoder.drain();
        require(files.frames==submitted,"encoded frame count differs from submitted count");
        require(files.keys>=2,"no second IDR; recovery cannot be validated");
        meter.write(files,capture,submitted,began);files.finish(opt);
        return 0;
    } catch(const probe::CaptureStall& e) {
        diagnostic("encoder progress timeout",HRESULT_FROM_WIN32(WAIT_TIMEOUT));
        std::cerr<<"CAPTURE_EXIT code=75 reason="<<e.what()<<"; retryable stall, all process resources reclaimed\n";
        return 75;
    } catch(const NativeFailure& e) {
        std::cerr<<"CAPTURE_EXIT code="<<(e.retry?75:1)<<" reason="<<e.what()<<"; all process resources will be reclaimed\n";
        return e.retry?75:1;
    } catch(const std::exception& e) {
        diagnostic("unclassified failure",E_FAIL);
        std::cerr<<"CAPTURE_EXIT code=1 reason="<<e.what()<<"\nFAILED: "<<e.what()<<"\nPartial files are NOT a successful validation; use a new output prefix after fixing the cause.\n";
        return 1;
    }
}

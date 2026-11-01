# 只观察显示电源、当前会话及锁定状态，不请求亮屏，不修改电源/安全设置；固定观察180秒。
$ErrorActionPreference='Stop'
Add-Type -ReferencedAssemblies @('System.dll','System.Windows.Forms.dll') -TypeDefinition @'
using System;
using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Threading;
using System.Text;
using System.Windows.Forms;
public sealed class RemoteDisplayObserver : NativeWindow, IDisposable {
    private IntPtr power;
    private bool sessions;
    private static readonly Guid ConsoleDisplay=new Guid("6FE69556-704A-47A0-8F24-C28D936FDA47");
    // RegisterPowerSettingNotification 将控制台显示状态事件发送给本探针的隐藏窗口。
    [DllImport("user32.dll",SetLastError=true)] private static extern IntPtr RegisterPowerSettingNotification(IntPtr recipient,ref Guid setting,uint flags);
    // UnregisterPowerSettingNotification 取消本次观察，不遗留通知句柄。
    [DllImport("user32.dll",SetLastError=true)] private static extern bool UnregisterPowerSettingNotification(IntPtr handle);
    // WTSRegisterSessionNotification 仅订阅当前会话的锁定、解锁及连接状态变化。
    [DllImport("wtsapi32.dll",SetLastError=true)] private static extern bool WTSRegisterSessionNotification(IntPtr window,uint flags);
    // WTSUnRegisterSessionNotification 在结束时取消本窗口订阅。
    [DllImport("wtsapi32.dll")] private static extern bool WTSUnRegisterSessionNotification(IntPtr window);
    // WTSQuerySessionInformation 只读当前会话的扩展状态，不输出用户名等个人信息。
    [DllImport("wtsapi32.dll",EntryPoint="WTSQuerySessionInformationW",SetLastError=true)] private static extern bool WTSQuerySessionInformation(IntPtr server,int session,int kind,out IntPtr buffer,out int bytes);
    // WTSFreeMemory 归还系统查询缓冲区。
    [DllImport("wtsapi32.dll")] private static extern void WTSFreeMemory(IntPtr buffer);
    // WTSGetActiveConsoleSessionId 取得当前物理控制台会话编号供日志对照。
    [DllImport("kernel32.dll")] private static extern uint WTSGetActiveConsoleSessionId();
    // OpenInputDesktop 只读查询当前输入桌面，失败时保留错误而不切换桌面。
    [DllImport("user32.dll",SetLastError=true)] private static extern IntPtr OpenInputDesktop(uint flags,bool inherit,uint access);
    // GetUserObjectInformation 查询桌面名称，不读取窗口或用户输入。
    [DllImport("user32.dll",EntryPoint="GetUserObjectInformationW",CharSet=CharSet.Unicode,SetLastError=true)] private static extern bool GetUserObjectInformation(IntPtr handle,int index,StringBuilder name,uint bytes,out uint needed);
    // CloseDesktop 释放只读桌面查询句柄。
    [DllImport("user32.dll")] private static extern bool CloseDesktop(IntPtr desktop);
    // RemoteDisplayObserver 创建不显示的消息窗口，只订阅事件，不执行开屏行为。
    public RemoteDisplayObserver() {
        CreateHandle(new CreateParams());
        Guid setting=ConsoleDisplay;
        power=RegisterPowerSettingNotification(Handle,ref setting,0);
        if(power==IntPtr.Zero) {int error=Marshal.GetLastWin32Error();DestroyHandle();throw new Exception("RegisterPowerSettingNotification failed: "+error);}
        sessions=WTSRegisterSessionNotification(Handle,0);
        if(!sessions) Console.WriteLine("WTS notifications unavailable: {0}; polling still enabled",Marshal.GetLastWin32Error());
    }
    // WndProc 记录系统报告的显示状态及会话事件，不把dim或off解释成已锁屏。
    protected override void WndProc(ref Message m) {
        try {
            if(m.Msg==0x218 && m.WParam.ToInt64()==0x8013 && m.LParam!=IntPtr.Zero) {
                Guid setting=(Guid)Marshal.PtrToStructure(m.LParam,typeof(Guid));
                if(setting==ConsoleDisplay && Marshal.ReadInt32(m.LParam,16)==4) {
                    int state=Marshal.ReadInt32(m.LParam,20);
                    Console.WriteLine("{0:o} DISPLAY state={1} ({2}); {3}",DateTime.UtcNow,state,state==0?"off":state==1?"on":state==2?"dim":"unknown",Snapshot());
                }
            }
            if(m.Msg==0x2b1) Console.WriteLine("{0:o} SESSION event={1} session={2} (7=lock,8=unlock); {3}",DateTime.UtcNow,m.WParam.ToInt64(),m.LParam.ToInt64(),Snapshot());
        } catch(Exception e) {Console.WriteLine("observer event error: "+e.Message);}
        base.WndProc(ref m);
    }
    // Snapshot 按Windows x64 SDK的WTSINFOEX前缀读取锁定标志，未知/不可用明确报告，不凭桌面名称推断。
    private static string Snapshot() {
        int id=Process.GetCurrentProcess().SessionId;
        IntPtr buffer;int bytes;
        string state="unknown";
        if(WTSQuerySessionInformation(IntPtr.Zero,id,25,out buffer,out bytes)) {
            try {
                // x64 WTSINFOEXW.Level位于0，按8字节对齐的Data从8开始；SessionFlags位于Data+8。
                if(IntPtr.Size==8 && bytes>=20 && Marshal.ReadInt32(buffer,0)==1) {
                    int flags=Marshal.ReadInt32(buffer,16);
                    state=flags==0?"locked":flags==1?"unlocked":"unknown:"+flags;
                }
            } finally {WTSFreeMemory(buffer);}
        } else state="query-error:"+Marshal.GetLastWin32Error();
        string input="unknown";
        IntPtr desktop=OpenInputDesktop(0,false,1);
        if(desktop==IntPtr.Zero) input="denied:"+Marshal.GetLastWin32Error();
        else {
            try {var name=new StringBuilder(256);uint needed;if(GetUserObjectInformation(desktop,2,name,512,out needed)) input=name.ToString();else input="query-error:"+Marshal.GetLastWin32Error();}
            finally {CloseDesktop(desktop);}
        }
        return "session="+id+" console="+WTSGetActiveConsoleSessionId()+" lock="+state+" input_desktop="+input;
    }
    // Dispose 依次注销事件并销毁观察窗口，不改变显示器当前状态。
    public void Dispose() {
        if(sessions) {WTSUnRegisterSessionNotification(Handle);sessions=false;}
        if(power!=IntPtr.Zero) {UnregisterPowerSettingNotification(power);power=IntPtr.Zero;}
        if(Handle!=IntPtr.Zero) DestroyHandle();
    }
    // Run 在当前线程泵消息180秒并打印状态变化，返回后关闭全部订阅。
    public static void Run() {
        using(var observer=new RemoteDisplayObserver()) {
            string last=Snapshot();Console.WriteLine("{0:o} START {1}; observing 180s, no display request",DateTime.UtcNow,last);
            var time=Stopwatch.StartNew();long next=0;
            while(time.ElapsedMilliseconds<180000) {
                Application.DoEvents();Thread.Sleep(50);
                if(time.ElapsedMilliseconds>=next) {next=time.ElapsedMilliseconds+1000;string state=Snapshot();if(state!=last) {Console.WriteLine("{0:o} STATE {1}",DateTime.UtcNow,state);last=state;}}
            }
        }
        Console.WriteLine("{0:o} observer stopped",DateTime.UtcNow);
    }
}
'@
[RemoteDisplayObserver]::Run()

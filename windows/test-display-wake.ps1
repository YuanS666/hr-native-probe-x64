# 独立验证显示空闲请求和显式开屏命令；不修改电源计划，不注入输入，不解锁桌面。
$ErrorActionPreference='Stop'
Add-Type -TypeDefinition @'
using System;
using System.Text;
using System.Runtime.InteropServices;
public static class RemoteDisplayProbe {
    // SetThreadExecutionState 在当前线程申请或清除显示空闲请求，返回零表示失败。
    [DllImport("kernel32.dll", SetLastError=true)] public static extern uint SetThreadExecutionState(uint flags);
    // GetDesktopWindow 取得单个桌面窗口，避免广播给所有应用造成累计超时。
    [DllImport("user32.dll")] public static extern IntPtr GetDesktopWindow();
    // SendMessageTimeout 显式请求显示器开启，最多等待1500毫秒，不发送键鼠输入。
    [DllImport("user32.dll", EntryPoint="SendMessageTimeoutW",SetLastError=true)] public static extern IntPtr SendMessageTimeout(IntPtr hwnd,uint msg,UIntPtr wparam,IntPtr lparam,uint flags,uint timeout,out UIntPtr result);
    // OpenInputDesktop 仅申请桌面名称查询权限，不切换或打开安全桌面进行控制。
    [DllImport("user32.dll", SetLastError=true)] public static extern IntPtr OpenInputDesktop(uint flags,bool inherit,uint access);
    // GetUserObjectInformation 查询桌面名称以确保只对普通Default桌面进行测试。
    [DllImport("user32.dll", EntryPoint="GetUserObjectInformationW",CharSet=CharSet.Unicode,SetLastError=true)] public static extern bool GetUserObjectInformation(IntPtr handle,int index,StringBuilder name,uint bytes,out uint needed);
    // CloseDesktop 释放本探针的桌面查询句柄。
    [DllImport("user32.dll")] public static extern bool CloseDesktop(IntPtr desktop);
    // Run 在同一线程完成等待、查询、显示请求及撤销，不让脚本线程迁移影响执行状态。
    public static void Run(int delay,int hold,string mode) {
        if(mode!="baseline" && mode!="hold" && mode!="wake") throw new ArgumentException("mode must be baseline, hold or wake");
        Console.WriteLine("Wait {0}s; disconnect AnyDesk and wait for the original 1-minute display timeout. Keep the Agent available.",delay);
        System.Threading.Thread.Sleep(delay*1000);
        IntPtr desktop=OpenInputDesktop(0,false,1);
        if(desktop==IntPtr.Zero) throw new Exception("OpenInputDesktop failed: "+Marshal.GetLastWin32Error());
        try {
            var name=new StringBuilder(256);uint needed;
            if(!GetUserObjectInformation(desktop,2,name,512,out needed)||name.ToString()!="Default") throw new Exception("Not ordinary Default input desktop; test stopped.");
        } finally {CloseDesktop(desktop);}
        if(mode=="baseline") {
            Console.WriteLine("{0:o} BASELINE: no display request, no wake command",DateTime.UtcNow);
            System.Threading.Thread.Sleep(hold*2000);return;
        }
        Console.WriteLine("{0:o} phase=A SetThreadExecutionState display request; mode={1}",DateTime.UtcNow,mode);
        uint previous=SetThreadExecutionState(0x80000002u);
        if(previous==0) throw new Exception("SetThreadExecutionState failed: "+Marshal.GetLastWin32Error());
        try {
            Console.WriteLine("API accepted, not proof of wake. Hold request {0}s; observe whether Agent recovers.",hold);
            System.Threading.Thread.Sleep(hold*1000);
            if(mode=="wake") {
            // 开屏前再次查询，避免等待期间桌面切换后仍尝试向锁屏桌面发送命令。
            desktop=OpenInputDesktop(0,false,1);
            if(desktop==IntPtr.Zero) throw new Exception("Input desktop unavailable before phase B.");
            try {
                var name=new StringBuilder(256);uint needed;
                if(!GetUserObjectInformation(desktop,2,name,512,out needed)||name.ToString()!="Default") throw new Exception("Desktop changed; phase B stopped.");
            } finally {CloseDesktop(desktop);}
            Console.WriteLine("{0:o} phase=B WM_SYSCOMMAND / SC_MONITORPOWER / -1",DateTime.UtcNow);
            UIntPtr result;
            IntPtr ok=SendMessageTimeout(GetDesktopWindow(),0x112,new UIntPtr(0xf170),new IntPtr(-1),0x23,1500,out result);
            Console.WriteLine("SendMessageTimeout returned={0}; message result={1}. Only returned=0 means send failed/timeout; message result=0 can be normal. Send success is NOT proof of panel wake or DXGI recovery.",ok,result);
            } else {Console.WriteLine("{0:o} phase=B skipped: hold-only test",DateTime.UtcNow);}
            System.Threading.Thread.Sleep(hold*1000);
        } finally {
            uint cleared=SetThreadExecutionState(0x80000000u);
            Console.WriteLine("{0:o} request cleared={1}. Original power plan unchanged.",DateTime.UtcNow,cleared!=0);
        }
    }
}
'@
$testMode=$env:RD_DISPLAY_TEST_MODE
if([string]::IsNullOrWhiteSpace($testMode)) { $testMode='wake' }
[RemoteDisplayProbe]::Run(75,20,$testMode)

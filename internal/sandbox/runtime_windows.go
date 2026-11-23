//go:build windows

// =================实现Windowsappcontainer沙箱、powerShell启动、继承资源限制等功能==============
package sandbox

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// 这些常量用于调用Windows原生api
// WindowsAPI使用数字标志和枚举值表示不同的操作选项
// 这里把数字定义为有意义的名称，方便使用
const (
	extendedStartupInfoPresent = 0x00080000 // CreateProcessW使用扩展启动信息结构体startupinfoex
	startfUseStdHandles        = 0x00000100 // 告诉CreateProcessW：使用startupinfo中指定的标准输入、标准输出、标准错误句柄
	handleFlagInherit          = 0x00000001 // 句柄标志：允许句柄被子进程继承
	procThreadAttributeSecCaps = 0x00020009 // startupinfoex属性：指定新进程使用的AppContainer安全能力
	procThreadAttributeHandles = 0x00020002 // startupinfoex属性：指定允许子进程继承哪些句柄
	procThreadAttributeJobList = 0x0002000D // startupinfoex属性：指定新进程创建时要加入的Job Object
	jobObjectExtendedLimitInfo = 9          // SetInformationJobObject使用的类型：设置Job Object的扩展限制信息
	jobObjectCPURateControl    = 15         // SetInformationJobObject使用的类型：设置Job Object的cpu使用率控制信息
	jobLimitActiveProcess      = 0x00000008 // Job Object限制标志：限制Job中同时运行的进程数量
	jobLimitProcessMemory      = 0x00000100 // Job Object限制标志： 限制单个进程的内存使用量
	jobLimitJobMemory          = 0x00000200 // Job Object限制标志：限制整个Job的内存使用量
	jobLimitKillOnClose        = 0x00002000 // Job Object限制标志：当最后一个Job Object句柄关闭时，终止Job中仍在运行的进程
	jobCPURateEnable           = 0x00000001 // CPU控制标志：启用cpu使用率限制
	jobCPURateHardCap          = 0x00000004 // CPU控制标志：使用硬限制，限制Job的cpu使用率上限
	genericRead                = 0x80000000 // 文件访问权限：允许读取文件
	openExisting               = 3          // CreateFileW的打开方式：只打开已经存在的对象，不创建新对象
	fileAttributeNormal        = 0x00000080 // 文件属性：普通文件属性
	infinite                   = 0xffffffff // WaitForSingleObject的等待时间参数：表示无限等待，指导目标对象进入有信号状态
	errorAlreadyExists         = 0x800700B7 // HRESULT错误码：对应AppContainer Profile已经存在的情况
)

// 这里集中声明需要调用的WindowsDLL和DLL中的原生函数
var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	advapi32 = syscall.NewLazyDLL("advapi32.dll")
	userenv  = syscall.NewLazyDLL("userenv.dll")

	procCreatePipe                    = kernel32.NewProc("CreatePipe")
	procSetHandleInformation          = kernel32.NewProc("SetHandleInformation")
	procCreateFileW                   = kernel32.NewProc("CreateFileW")
	procCreateProcessW                = kernel32.NewProc("CreateProcessW")
	procInitializeProcThreadAttribute = kernel32.NewProc("InitializeProcThreadAttributeList")
	procUpdateProcThreadAttribute     = kernel32.NewProc("UpdateProcThreadAttribute")
	procDeleteProcThreadAttribute     = kernel32.NewProc("DeleteProcThreadAttributeList")
	procCreateJobObjectW              = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject       = kernel32.NewProc("SetInformationJobObject")
	procWaitForSingleObject           = kernel32.NewProc("WaitForSingleObject")
	procGetExitCodeProcess            = kernel32.NewProc("GetExitCodeProcess")
	procCloseHandle                   = kernel32.NewProc("CloseHandle")
	procLocalFree                     = kernel32.NewProc("LocalFree")
	procFreeSid                       = advapi32.NewProc("FreeSid")
	procConvertSidToStringSidW        = advapi32.NewProc("ConvertSidToStringSidW")
	procCreateAppContainerProfile     = userenv.NewProc("CreateAppContainerProfile")
	procDeriveAppContainerSid         = userenv.NewProc("DeriveAppContainerSidFromAppContainerName")
	procDeleteAppContainerProfile     = userenv.NewProc("DeleteAppContainerProfile")
)

type securityAttributes struct {
	Length             uint32
	SecurityDescriptor uintptr
	InheritHandle      int32
}

type securityCapabilities struct {
	AppContainerSID uintptr
	Capabilities    uintptr
	CapabilityCount uint32
	Reserved        uint32
}

type startupInfo struct {
	CB            uint32
	Reserved      *uint16
	Desktop       *uint16
	Title         *uint16
	X             uint32
	Y             uint32
	XSize         uint32
	YSize         uint32
	XCountChars   uint32
	YCountChars   uint32
	FillAttribute uint32
	Flags         uint32
	ShowWindow    uint16
	Reserved2Size uint16
	Reserved2     *byte
	StdInput      syscall.Handle
	StdOutput     syscall.Handle
	StdErr        syscall.Handle
}

type startupInfoEx struct {
	StartupInfo   startupInfo
	AttributeList uintptr
}

type processInformation struct {
	Process   syscall.Handle
	Thread    syscall.Handle
	ProcessID uint32
	ThreadID  uint32
}

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type jobObjectExtendedLimitInformation struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IOInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

type jobObjectCPURateControlInformation struct {
	ControlFlags uint32
	CPURate      uint32
}

// WindowsRuntime 为每个 Shell 任务使用独立的 Windows AppContainer(权限沙箱) 和 Job Object(资源限制)
// AppContainer 只获得访问所选工作区的权限，不授予网络 capability。
type WindowsRuntime struct{}

func NewWindowsRuntime() (*WindowsRuntime, error) {
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		return nil, fmt.Errorf("Windows PowerShell 不可用: %w", err)
	}
	return &WindowsRuntime{}, nil
}

func Available() bool {
	_, err := NewWindowsRuntime()
	return err == nil
}

// RunnerMain 是 ProcessManager 使用的内部子进程入口。
// args 必须严格采用以下格式：-- <PowerShell command>。
func RunnerMain(args []string) int {
	if len(args) != 2 || args[0] != "--" {
		fmt.Fprintln(os.Stderr, "sandbox runner 参数无效")
		return 2
	}
	if err := runAppContainer(args[1], mustWorkingDirectory()); err != nil {
		fmt.Fprintln(os.Stderr, "Windows AppContainer 执行失败:", err)
		return 1
	}
	return 0
}

func mustWorkingDirectory() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return cwd
}

func runAppContainer(command, workDir string) (runErr error) {
	if strings.TrimSpace(command) == "" || workDir == "" || !filepath.IsAbs(workDir) {
		return errors.New("命令或工作目录无效")
	}
	if len(command) > 30000 {
		return errors.New("Shell 命令超过 Windows 命令行长度限制")
	}
	workDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return fmt.Errorf("解析工作目录失败: %w", err)
	}

	profileName, err := uniqueProfileName()
	if err != nil {
		return err
	}
	sid, err := createProfile(profileName)
	if err != nil {
		return err
	}
	defer func() {
		// AppContainer API 返回的 SID 由操作系统分配。
		procFreeSid.Call(sid)
		name, _ := syscall.UTF16PtrFromString(profileName)
		procDeleteAppContainerProfile.Call(uintptr(unsafe.Pointer(name)))
	}()

	sidText, err := sidString(sid)
	if err != nil {
		return err
	}
	if err := grantWorkspaceAccess(workDir, sidText); err != nil {
		return err
	}
	defer func() {
		if err := revokeWorkspaceAccess(workDir, sidText); err != nil && runErr == nil {
			runErr = fmt.Errorf("移除 AppContainer 工作区 ACL 失败: %w", err)
		}
	}()

	return launchRestrictedPowerShell(command, workDir, sid)
}

func uniqueProfileName() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "OpenAI.CodeAgent." + hex.EncodeToString(random[:]), nil
}

func createProfile(name string) (uintptr, error) {
	namePtr, _ := syscall.UTF16PtrFromString(name)
	displayPtr, _ := syscall.UTF16PtrFromString("Code Agent task sandbox")
	descPtr, _ := syscall.UTF16PtrFromString("Ephemeral AppContainer for one Code Agent Shell task")
	var sid uintptr
	hr, _, _ := procCreateAppContainerProfile.Call(
		uintptr(unsafe.Pointer(namePtr)), uintptr(unsafe.Pointer(displayPtr)),
		uintptr(unsafe.Pointer(descPtr)), 0, 0, uintptr(unsafe.Pointer(&sid)),
	)
	if hr == 0 {
		return sid, nil
	}
	if hr != errorAlreadyExists {
		return 0, fmt.Errorf("CreateAppContainerProfile HRESULT=0x%08x", uint32(hr))
	}
	hr, _, _ = procDeriveAppContainerSid.Call(uintptr(unsafe.Pointer(namePtr)), uintptr(unsafe.Pointer(&sid)))
	if hr != 0 {
		return 0, fmt.Errorf("DeriveAppContainerSidFromAppContainerName HRESULT=0x%08x", uint32(hr))
	}
	return sid, nil
}

func sidString(sid uintptr) (string, error) {
	var text *uint16
	ok, _, callErr := procConvertSidToStringSidW.Call(sid, uintptr(unsafe.Pointer(&text)))
	if ok == 0 {
		return "", fmt.Errorf("ConvertSidToStringSidW: %w", callErr)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(text)))
	return utf16PointerString(text), nil
}

func buildRestrictedEnvironment(tempDir, powerShellHome string) ([]uint16, error) {
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		systemRoot = os.Getenv("WINDIR")
	}
	if systemRoot == "" {
		return nil, errors.New("无法确定 Windows SystemRoot")
	}
	values := map[string]string{
		"SystemRoot":   systemRoot,
		"WINDIR":       systemRoot,
		"PATH":         os.Getenv("PATH"),
		"PATHEXT":      ".COM;.EXE;.BAT;.CMD",
		"COMSPEC":      filepath.Join(systemRoot, "System32", "cmd.exe"),
		"TEMP":         tempDir,
		"TMP":          tempDir,
		"USERPROFILE":  tempDir,
		"HOME":         tempDir,
		"APPDATA":      filepath.Join(tempDir, "AppData"),
		"LOCALAPPDATA": filepath.Join(tempDir, "LocalAppData"),
		"PSModulePath": filepath.Join(powerShellHome, "Modules"),
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return strings.ToUpper(keys[i]) < strings.ToUpper(keys[j]) })
	var block []uint16
	for _, key := range keys {
		entry, err := syscall.UTF16FromString(key + "=" + values[key])
		if err != nil {
			return nil, err
		}
		block = append(block, entry[:len(entry)-1]...)
		block = append(block, 0)
	}
	block = append(block, 0)
	return block, nil
}

func utf16PointerString(value *uint16) string {
	if value == nil {
		return ""
	}
	units := make([]uint16, 0, 64)
	for index := uintptr(0); index < 32768; index++ {
		unit := *(*uint16)(unsafe.Add(unsafe.Pointer(value), index*2))
		if unit == 0 {
			break
		}
		units = append(units, unit)
	}
	return syscall.UTF16ToString(units)
}

func grantWorkspaceAccess(workDir, sid string) error {
	acl := "*" + sid + ":(OI)(CI)M"
	cmd := exec.Command("icacls.exe", workDir, "/grant", acl, "/T", "/C", "/Q")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("授予 AppContainer 工作区访问权限失败: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func revokeWorkspaceAccess(workDir, sid string) error {
	cmd := exec.Command("icacls.exe", workDir, "/remove:g", "*"+sid, "/T", "/C", "/Q")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("icacls: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func launchRestrictedPowerShell(command, workDir string, appContainerSID uintptr) (runErr error) {
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		return err
	}
	appPtr, _ := syscall.UTF16PtrFromString(powershell)
	workPtr, _ := syscall.UTF16PtrFromString(workDir)
	args := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command}
	commandLine := syscall.EscapeArg(powershell)
	for _, arg := range args {
		commandLine += " " + syscall.EscapeArg(arg)
	}
	commandLinePtr, _ := syscall.UTF16PtrFromString(commandLine)

	// 在已授权工作区内为子进程创建独立的临时主目录。
	tempDir, err := os.MkdirTemp(workDir, ".codeagent-tmp-")
	if err != nil {
		return fmt.Errorf("创建沙箱临时目录失败: %w", err)
	}
	defer os.RemoveAll(tempDir)
	for _, name := range []string{"AppData", "LocalAppData", "Temp"} {
		if err := os.MkdirAll(filepath.Join(tempDir, name), 0o700); err != nil {
			return err
		}
	}
	environment, err := buildRestrictedEnvironment(tempDir, filepath.Dir(powershell))
	if err != nil {
		return err
	}

	stdinName, _ := syscall.UTF16PtrFromString("NUL")
	var stdin syscall.Handle
	stdinSecurity := securityAttributes{Length: uint32(unsafe.Sizeof(securityAttributes{})), InheritHandle: 1}
	stdinRaw, _, callErr := procCreateFileW.Call(uintptr(unsafe.Pointer(stdinName)), genericRead, 0x1|0x2, uintptr(unsafe.Pointer(&stdinSecurity)), openExisting, fileAttributeNormal, 0)
	runtime.KeepAlive(&stdinSecurity)
	stdin = syscall.Handle(stdinRaw)
	if stdin == syscall.InvalidHandle {
		return fmt.Errorf("打开 NUL 标准输入失败: %w", callErr)
	}
	defer func() {
		if stdin != syscall.InvalidHandle {
			closeHandle(stdin)
		}
	}()

	stdoutRead, stdoutWrite, err := createPipe()
	if err != nil {
		return fmt.Errorf("创建 stdout pipe: %w", err)
	}
	defer func() { closeHandle(stdoutRead) }()
	defer func() { closeHandle(stdoutWrite) }()
	stderrRead, stderrWrite, err := createPipe()
	if err != nil {
		return fmt.Errorf("创建 stderr pipe: %w", err)
	}
	defer func() { closeHandle(stderrRead) }()
	defer func() { closeHandle(stderrWrite) }()
	if err := setNonInheritable(stdoutRead); err != nil {
		return err
	}
	if err := setNonInheritable(stderrRead); err != nil {
		return err
	}

	job, _, err := procCreateJobObjectW.Call(0, 0)
	if job == 0 {
		return fmt.Errorf("CreateJobObjectW: %w", err)
	}
	defer func() {
		if job != 0 {
			closeHandle(syscall.Handle(job))
		}
	}()
	if err := configureJob(syscall.Handle(job)); err != nil {
		return err
	}

	attributeList, attributeBuffer, err := newAttributeList(3)
	if err != nil {
		return err
	}
	defer procDeleteProcThreadAttribute.Call(attributeList)
	capabilities := securityCapabilities{AppContainerSID: appContainerSID}
	ok, _, callErr := procUpdateProcThreadAttribute.Call(attributeList, 0, procThreadAttributeSecCaps, uintptr(unsafe.Pointer(&capabilities)), unsafe.Sizeof(capabilities), 0, 0)
	if ok == 0 {
		return fmt.Errorf("设置 AppContainer 安全能力失败: %w", callErr)
	}
	handles := [3]syscall.Handle{stdin, stdoutWrite, stderrWrite}
	ok, _, callErr = procUpdateProcThreadAttribute.Call(attributeList, 0, procThreadAttributeHandles, uintptr(unsafe.Pointer(&handles[0])), unsafe.Sizeof(handles), 0, 0)
	if ok == 0 {
		return fmt.Errorf("限制继承句柄失败: %w", callErr)
	}
	jobList := [1]syscall.Handle{syscall.Handle(job)}
	ok, _, callErr = procUpdateProcThreadAttribute.Call(attributeList, 0, procThreadAttributeJobList, uintptr(unsafe.Pointer(&jobList[0])), unsafe.Sizeof(jobList), 0, 0)
	if ok == 0 {
		return fmt.Errorf("设置进程创建时 Job Object 绑定失败: %w", callErr)
	}

	startup := startupInfoEx{AttributeList: attributeList}
	startup.StartupInfo.CB = uint32(unsafe.Sizeof(startup))
	startup.StartupInfo.Flags = startfUseStdHandles
	startup.StartupInfo.StdInput = stdin
	startup.StartupInfo.StdOutput = stdoutWrite
	startup.StartupInfo.StdErr = stderrWrite
	var process processInformation
	flags := uintptr(extendedStartupInfoPresent | 0x00000400)
	created, _, callErr := procCreateProcessW.Call(
		uintptr(unsafe.Pointer(appPtr)), uintptr(unsafe.Pointer(commandLinePtr)),
		0, 0, 1, flags, uintptr(unsafe.Pointer(&environment[0])), uintptr(unsafe.Pointer(workPtr)),
		uintptr(unsafe.Pointer(&startup)), uintptr(unsafe.Pointer(&process)),
	)
	runtime.KeepAlive(environment)
	runtime.KeepAlive(attributeBuffer)
	runtime.KeepAlive(&capabilities)
	runtime.KeepAlive(&handles)
	runtime.KeepAlive(&jobList)
	runtime.KeepAlive(&startup)
	if created == 0 {
		return fmt.Errorf("在 AppContainer 中创建 PowerShell 失败: %w", callErr)
	}
	defer closeHandle(process.Thread)
	defer closeHandle(process.Process)
	// 子进程只继承三个标准句柄。父进程应立即关闭对应的写入端，
	// 这样沙箱进程树退出后，读取端才能收到 EOF。
	closeHandle(stdin)
	closeHandle(stdoutWrite)
	closeHandle(stderrWrite)
	stdin = syscall.InvalidHandle
	stdoutWrite = syscall.InvalidHandle
	stderrWrite = syscall.InvalidHandle

	stdoutFile := os.NewFile(uintptr(stdoutRead), "sandbox-stdout")
	stderrFile := os.NewFile(uintptr(stderrRead), "sandbox-stderr")
	stdoutRead = syscall.InvalidHandle
	stderrRead = syscall.InvalidHandle
	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); _, _ = io.Copy(os.Stdout, stdoutFile); _ = stdoutFile.Close() }()
	go func() { defer readers.Done(); _, _ = io.Copy(os.Stderr, stderrFile); _ = stderrFile.Close() }()

	wait, _, callErr := procWaitForSingleObject.Call(uintptr(process.Process), infinite)
	if wait != 0 {
		runErr = fmt.Errorf("等待 AppContainer 进程结束失败: %w", callErr)
	}
	// 关闭 Job Object 时，会终止仍未随 PowerShell 父进程退出的子进程。
	closeHandle(syscall.Handle(job))
	job = 0
	readers.Wait()
	var exitCode uint32
	if ok, _, callErr := procGetExitCodeProcess.Call(uintptr(process.Process), uintptr(unsafe.Pointer(&exitCode))); ok == 0 && runErr == nil {
		runErr = fmt.Errorf("GetExitCodeProcess: %w", callErr)
	}
	if exitCode != 0 && runErr == nil {
		runErr = fmt.Errorf("PowerShell 退出码: %d", exitCode)
	}
	return runErr
}

func createPipe() (syscall.Handle, syscall.Handle, error) {
	security := securityAttributes{Length: uint32(unsafe.Sizeof(securityAttributes{})), InheritHandle: 1}
	var readHandle, writeHandle syscall.Handle
	ok, _, err := procCreatePipe.Call(uintptr(unsafe.Pointer(&readHandle)), uintptr(unsafe.Pointer(&writeHandle)), uintptr(unsafe.Pointer(&security)), 0)
	runtime.KeepAlive(&security)
	if ok == 0 {
		return 0, 0, err
	}
	return readHandle, writeHandle, nil
}

func setNonInheritable(handle syscall.Handle) error {
	ok, _, err := procSetHandleInformation.Call(uintptr(handle), handleFlagInherit, 0)
	if ok == 0 {
		return err
	}
	return nil
}

func newAttributeList(count uintptr) (uintptr, []byte, error) {
	var size uintptr
	procInitializeProcThreadAttribute.Call(0, count, 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		return 0, nil, errors.New("获取进程属性列表大小失败")
	}
	buffer := make([]byte, size)
	list := uintptr(unsafe.Pointer(&buffer[0]))
	ok, _, err := procInitializeProcThreadAttribute.Call(list, count, 0, uintptr(unsafe.Pointer(&size)))
	if ok == 0 {
		return 0, nil, fmt.Errorf("初始化进程属性列表失败: %w", err)
	}
	return list, buffer, nil
}

func configureJob(job syscall.Handle) error {
	limits := jobObjectExtendedLimitInformation{}
	limits.BasicLimitInformation.LimitFlags = jobLimitKillOnClose | jobLimitActiveProcess | jobLimitProcessMemory | jobLimitJobMemory
	limits.BasicLimitInformation.ActiveProcessLimit = 64
	limits.ProcessMemoryLimit = 1 << 30
	limits.JobMemoryLimit = 2 << 30
	ok, _, err := procSetInformationJobObject.Call(uintptr(job), jobObjectExtendedLimitInfo, uintptr(unsafe.Pointer(&limits)), unsafe.Sizeof(limits))
	if ok == 0 {
		return fmt.Errorf("设置 Job Object 进程和内存限制失败: %w", err)
	}
	cpu := jobObjectCPURateControlInformation{ControlFlags: jobCPURateEnable | jobCPURateHardCap, CPURate: 2000}
	ok, _, err = procSetInformationJobObject.Call(uintptr(job), jobObjectCPURateControl, uintptr(unsafe.Pointer(&cpu)), unsafe.Sizeof(cpu))
	if ok == 0 {
		return fmt.Errorf("设置 Job Object CPU 限制失败: %w", err)
	}
	return nil
}

func closeHandle(handle syscall.Handle) {
	if handle != 0 && handle != syscall.InvalidHandle {
		procCloseHandle.Call(uintptr(handle))
	}
}

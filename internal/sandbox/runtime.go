// ==============定义沙箱不可用错误===================
package sandbox

import "errors"

// 定义全局变量，沙箱不可用错误
var ErrSandboxUnavailable = errors.New("当前平台或系统环境不支持所需的操作系统沙箱")

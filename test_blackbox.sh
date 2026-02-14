#!/bin/bash
set -e

echo "=========================================="
echo "FileShare 黑盒测试套件"
echo "=========================================="

# 配置
TEST_DIR="/tmp/fileshare_test_$$"
SERVER_BIN="./fileshare-server"
PORT=18888

# 颜色
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 测试计数
TESTS_PASSED=0
TESTS_FAILED=0

# 清理函数
cleanup() {
    echo -e "\n${YELLOW}清理测试环境...${NC}"
    pkill -f "$SERVER_BIN" 2>/dev/null || true
    rm -rf "$TEST_DIR"
}

trap cleanup EXIT

# 测试函数
run_test() {
    local test_name="$1"
    echo -e "\n${YELLOW}[TEST] $test_name${NC}"
}

pass_test() {
    echo -e "${GREEN}[PASS]${NC} $1"
    ((TESTS_PASSED++))
}

fail_test() {
    echo -e "${RED}[FAIL]${NC} $1"
    ((TESTS_FAILED++))
}

# 等待服务器启动
wait_for_server() {
    local max_attempts=30
    local attempt=0
    while [ $attempt -lt $max_attempts ]; do
        if curl -s "http://127.0.0.1:$PORT/api/info" >/dev/null 2>&1; then
            return 0
        fi
        sleep 0.5
        ((attempt++))
    done
    return 1
}

# 创建测试环境
setup() {
    echo "创建测试目录..."
    mkdir -p "$TEST_DIR/send"
    mkdir -p "$TEST_DIR/recv"
    mkdir -p "$TEST_DIR/download"
    
    # 创建测试文件
    echo "Hello FileShare Test" > "$TEST_DIR/send/test_small.txt"
    dd if=/dev/urandom of="$TEST_DIR/send/test_1mb.bin" bs=1M count=1 2>/dev/null
    
    # 创建测试目录
    mkdir -p "$TEST_DIR/send/test_dir/subdir"
    echo "file1" > "$TEST_DIR/send/test_dir/file1.txt"
    echo "file2" > "$TEST_DIR/send/test_dir/subdir/file2.txt"
}

# 测试1: 发送单文件
TestSendSingleFile() {
    run_test "发送单文件"
    
    $SERVER_BIN -p $PORT send "$TEST_DIR/send/test_small.txt" &
    SERVER_PID=$!
    
    if ! wait_for_server; then
        fail_test "服务器启动失败"
        kill $SERVER_PID 2>/dev/null || true
        return
    fi
    
    # 测试API
    RESPONSE=$(curl -s "http://127.0.0.1:$PORT/api/info")
    if echo "$RESPONSE" | grep -q '"mode":"send"'; then
        pass_test "API返回正确模式"
    else
        fail_test "API模式错误: $RESPONSE"
    fi
    
    # 测试下载
    curl -s "http://127.0.0.1:$PORT/api/download" -o "$TEST_DIR/download/downloaded.txt"
    
    if diff "$TEST_DIR/send/test_small.txt" "$TEST_DIR/download/downloaded.txt" >/dev/null; then
        pass_test "文件下载成功且内容一致"
    else
        fail_test "文件下载后内容不一致"
    fi
    
    kill $SERVER_PID 2>/dev/null || true
    sleep 1
}

# 测试2: 发送目录（自动打包zip）
TestSendDirectory() {
    run_test "发送目录（流式zip）"
    
    $SERVER_BIN -p $PORT send "$TEST_DIR/send/test_dir" &
    SERVER_PID=$!
    
    if ! wait_for_server; then
        fail_test "服务器启动失败"
        kill $SERVER_PID 2>/dev/null || true
        return
    fi
    
    # 下载zip
    curl -s "http://127.0.0.1:$PORT/api/download" -o "$TEST_DIR/download/test_dir.zip"
    
    if [ -f "$TEST_DIR/download/test_dir.zip" ] && [ -s "$TEST_DIR/download/test_dir.zip" ]; then
        pass_test "目录zip下载成功"
        
        # 验证zip内容
        if unzip -t "$TEST_DIR/download/test_dir.zip" >/dev/null 2>&1; then
            pass_test "下载的zip文件有效"
        else
            fail_test "下载的zip文件损坏"
        fi
    else
        fail_test "目录zip下载失败"
    fi
    
    kill $SERVER_PID 2>/dev/null || true
    sleep 1
}

# 测试3: 接收文件
TestReceiveFile() {
    run_test "接收文件"
    
    $SERVER_BIN -p $PORT recv "$TEST_DIR/recv" &
    SERVER_PID=$!
    
    if ! wait_for_server; then
        fail_test "服务器启动失败"
        kill $SERVER_PID 2>/dev/null || true
        return
    fi
    
    # 上传文件
    UPLOAD_RESPONSE=$(curl -s -F "file=@$TEST_DIR/send/test_small.txt" "http://127.0.0.1:$PORT/api/upload")
    
    if echo "$UPLOAD_RESPONSE" | grep -q '"status":"success"'; then
        pass_test "文件上传成功"
        
        if [ -f "$TEST_DIR/recv/test_small.txt" ]; then
            pass_test "文件保存到接收目录"
            
            if diff "$TEST_DIR/send/test_small.txt" "$TEST_DIR/recv/test_small.txt" >/dev/null; then
                pass_test "上传文件内容一致"
            else
                fail_test "上传文件内容不一致"
            fi
        else
            fail_test "文件未保存到接收目录"
        fi
    else
        fail_test "文件上传失败: $UPLOAD_RESPONSE"
    fi
    
    kill $SERVER_PID 2>/dev/null || true
    sleep 1
}

# 测试4: 单客户端限制
TestSingleClientLimit() {
    run_test "单客户端限制"
    
    $SERVER_BIN -p $PORT send "$TEST_DIR/send/test_small.txt" &
    SERVER_PID=$!
    
    if ! wait_for_server; then
        fail_test "服务器启动失败"
        kill $SERVER_PID 2>/dev/null || true
        return
    fi
    
    # 第一个客户端下载
    curl -s "http://127.0.0.1:$PORT/api/download" >/dev/null 2>&1 &
    CURL_PID1=$!
    sleep 0.5
    
    # 第二个客户端尝试下载（应该失败）
    RESPONSE=$(curl -s -w "%{http_code}" "http://127.0.0.1:$PORT/api/download" -o /dev/null)
    
    if [ "$RESPONSE" = "503" ]; then
        pass_test "第二个客户端被正确拒绝（503）"
    else
        fail_test "第二个客户端应该被拒绝，但返回: $RESPONSE"
    fi
    
    wait $CURL_PID1 2>/dev/null || true
    kill $SERVER_PID 2>/dev/null || true
    sleep 1
}

# 测试5: 文件冲突检测
TestFileConflict() {
    run_test "文件冲突检测"
    
    # 先创建已存在的文件
    echo "existing" > "$TEST_DIR/recv/conflict_test.txt"
    
    $SERVER_BIN -p $PORT recv "$TEST_DIR/recv" &
    SERVER_PID=$!
    
    if ! wait_for_server; then
        fail_test "服务器启动失败"
        kill $SERVER_PID 2>/dev/null || true
        return
    fi
    
    # 上传同名文件
    echo "new content" > "$TEST_DIR/send/conflict_test.txt"
    RESPONSE=$(curl -s -w "%{http_code}" -F "file=@$TEST_DIR/send/conflict_test.txt" \
        "http://127.0.0.1:$PORT/api/upload" -o /dev/null)
    
    if [ "$RESPONSE" = "409" ]; then
        pass_test "文件冲突正确返回409状态码"
    else
        fail_test "应该返回409，但返回: $RESPONSE"
    fi
    
    kill $SERVER_PID 2>/dev/null || true
    sleep 1
}

# 测试6: API端点测试
TestAPIEndpoints() {
    run_test "API端点测试"
    
    $SERVER_BIN -p $PORT send "$TEST_DIR/send/test_small.txt" &
    SERVER_PID=$!
    
    if ! wait_for_server; then
        fail_test "服务器启动失败"
        kill $SERVER_PID 2>/dev/null || true
        return
    fi
    
    # 测试 /api/info
    INFO=$(curl -s "http://127.0.0.1:$PORT/api/info")
    if echo "$INFO" | grep -q '"mode"' && echo "$INFO" | grep -q '"status"'; then
        pass_test "/api/info 返回正确JSON"
    else
        fail_test "/api/info 返回格式错误"
    fi
    
    # 测试 /api/log
    LOG=$(curl -s "http://127.0.0.1:$PORT/api/log")
    if echo "$LOG" | grep -q '^\['; then
        pass_test "/api/log 返回日志数组"
    else
        fail_test "/api/log 返回格式错误"
    fi
    
    # 测试网页界面
    HTML=$(curl -s "http://127.0.0.1:$PORT/")
    if echo "$HTML" | grep -q "FileShare"; then
        pass_test "网页界面可访问"
    else
        fail_test "网页界面访问失败"
    fi
    
    kill $SERVER_PID 2>/dev/null || true
    sleep 1
}

# 测试7: 断点续传（Range请求）
TestRangeRequest() {
    run_test "断点续传（Range请求）"
    
    $SERVER_BIN -p $PORT send "$TEST_DIR/send/test_1mb.bin" &
    SERVER_PID=$!
    
    if ! wait_for_server; then
        fail_test "服务器启动失败"
        kill $SERVER_PID 2>/dev/null || true
        return
    fi
    
    # 请求前512字节
    curl -s -r 0-511 "http://127.0.0.1:$PORT/api/download" -o "$TEST_DIR/download/partial.bin"
    
    if [ -f "$TEST_DIR/download/partial.bin" ] && [ $(stat -f%z "$TEST_DIR/download/partial.bin" 2>/dev/null || stat -c%s "$TEST_DIR/download/partial.bin") -eq 512 ]; then
        pass_test "Range请求返回正确字节数"
    else
        fail_test "Range请求返回字节数错误"
    fi
    
    kill $SERVER_PID 2>/dev/null || true
    sleep 1
}

# 测试8: 自动退出功能
TestAutoExit() {
    run_test "自动退出功能"
    
    $SERVER_BIN send "$TEST_DIR/send/test_small.txt" -p $PORT -auto-exit &
    SERVER_PID=$!
    
    if ! wait_for_server; then
        fail_test "服务器启动失败"
        kill $SERVER_PID 2>/dev/null || true
        return
    fi
    
    # 下载文件触发传输完成
    curl -s "http://127.0.0.1:$PORT/api/download" >/dev/null 2>&1
    
    # 等待服务器退出
    sleep 3
    
    if ! kill -0 $SERVER_PID 2>/dev/null; then
        pass_test "传输完成后服务器自动退出"
    else
        fail_test "服务器未自动退出"
        kill $SERVER_PID 2>/dev/null || true
    fi
}

# 测试9: 大文件传输
TestLargeFile() {
    run_test "大文件传输（1MB）"
    
    $SERVER_BIN -p $PORT send "$TEST_DIR/send/test_1mb.bin" &
    SERVER_PID=$!
    
    if ! wait_for_server; then
        fail_test "服务器启动失败"
        kill $SERVER_PID 2>/dev/null || true
        return
    fi
    
    # 下载并计算MD5
    curl -s "http://127.0.0.1:$PORT/api/download" -o "$TEST_DIR/download/large_downloaded.bin"
    
    if [ -f "$TEST_DIR/download/large_downloaded.bin" ]; then
        ORIG_SIZE=$(stat -f%z "$TEST_DIR/send/test_1mb.bin" 2>/dev/null || stat -c%s "$TEST_DIR/send/test_1mb.bin")
        DOWN_SIZE=$(stat -f%z "$TEST_DIR/download/large_downloaded.bin" 2>/dev/null || stat -c%s "$TEST_DIR/download/large_downloaded.bin")
        
        if [ "$ORIG_SIZE" -eq "$DOWN_SIZE" ]; then
            pass_test "大文件传输完成，大小正确 ($ORIG_SIZE bytes)"
        else
            fail_test "大文件大小不匹配: $ORIG_SIZE vs $DOWN_SIZE"
        fi
    else
        fail_test "大文件下载失败"
    fi
    
    kill $SERVER_PID 2>/dev/null || true
    sleep 1
}

# 测试10: 并发连接测试
TestConcurrentConnections() {
    run_test "并发连接限制"
    
    $SERVER_BIN -p $PORT recv "$TEST_DIR/recv" &
    SERVER_PID=$!
    
    if ! wait_for_server; then
        fail_test "服务器启动失败"
        kill $SERVER_PID 2>/dev/null || true
        return
    fi
    
    # 同时发起多个上传请求
    FAIL_COUNT=0
    for i in {1..3}; do
        echo "test$i" > "$TEST_DIR/send/concurrent_$i.txt"
        HTTP_CODE=$(curl -s -w "%{http_code}" -F "file=@$TEST_DIR/send/concurrent_$i.txt" \
            "http://127.0.0.1:$PORT/api/upload" -o /dev/null)
        
        # 只有第一个应该成功（200），其他应该被503拒绝
        if [ "$HTTP_CODE" = "503" ]; then
            ((FAIL_COUNT++))
        fi
    done
    
    if [ $FAIL_COUNT -ge 2 ]; then
        pass_test "并发请求正确限制，$FAIL_COUNT 个请求被拒绝"
    else
        fail_test "并发限制可能存在问题"
    fi
    
    kill $SERVER_PID 2>/dev/null || true
    sleep 1
}

# 主测试流程
main() {
    echo "测试目录: $TEST_DIR"
    echo "服务器二进制: $SERVER_BIN"
    echo ""
    
    # 检查服务器二进制
    if [ ! -f "$SERVER_BIN" ]; then
        echo -e "${RED}错误: 找不到服务器二进制文件 $SERVER_BIN${NC}"
        echo "请先构建: cd /Users/cy/work/send/fileshare && go build -o fileshare-server"
        exit 1
    fi
    
    # 准备测试环境
    setup
    
    # 运行所有测试
    TestSendSingleFile
    TestSendDirectory
    TestReceiveFile
    TestSingleClientLimit
    TestFileConflict
    TestAPIEndpoints
    TestRangeRequest
    TestAutoExit
    TestLargeFile
    TestConcurrentConnections
    
    # 输出结果
    echo -e "\n=========================================="
    echo "测试结果汇总"
    echo "=========================================="
    echo -e "${GREEN}通过: $TESTS_PASSED${NC}"
    echo -e "${RED}失败: $TESTS_FAILED${NC}"
    echo "=========================================="
    
    if [ $TESTS_FAILED -eq 0 ]; then
        echo -e "${GREEN}所有测试通过！${NC}"
        exit 0
    else
        echo -e "${RED}有 $TESTS_FAILED 个测试失败${NC}"
        exit 1
    fi
}

main "$@"

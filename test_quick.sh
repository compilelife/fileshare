#!/bin/bash
set -e

echo "=========================================="
echo "FileShare 功能测试"
echo "=========================================="

TEST_DIR="/tmp/fs_quick_test_$$"
SERVER_BIN="./fileshare-server"
PORT=18888

# 清理
cleanup() {
    pkill -f "$SERVER_BIN" 2>/dev/null || true
    rm -rf "$TEST_DIR"
}

cleanup
mkdir -p "$TEST_DIR/send" "$TEST_DIR/recv" "$TEST_DIR/download"

# 创建测试文件
echo "Test content for file transfer" > "$TEST_DIR/send/test.txt"
dd if=/dev/urandom of="$TEST_DIR/send/large.bin" bs=1M count=1 2>/dev/null

PASS=0
FAIL=0

test_pass() {
    echo "✅ PASS: $1"
    ((PASS++))
}

test_fail() {
    echo "❌ FAIL: $1"
    ((FAIL++))
}

# 测试1: 基本下载
echo ""
echo "Test 1: 基本文件下载"
$SERVER_BIN -p $PORT send "$TEST_DIR/send/test.txt" &
PID=$!
sleep 2

RESPONSE=$(curl -s "http://127.0.0.1:$PORT/api/info")
if echo "$RESPONSE" | grep -q '"mode":"send"'; then
    test_pass "API 返回正确"
else
    test_fail "API 返回错误"
fi

curl -s "http://127.0.0.1:$PORT/api/download" -o "$TEST_DIR/download/test.txt"
if diff "$TEST_DIR/send/test.txt" "$TEST_DIR/download/test.txt" >/dev/null; then
    test_pass "文件下载成功"
else
    test_fail "文件下载失败"
fi

kill $PID 2>/dev/null || true
sleep 1

# 测试2: 文件上传
echo ""
echo "Test 2: 文件上传"
$SERVER_BIN -p $PORT recv "$TEST_DIR/recv" &
PID=$!
sleep 2

UPLOAD_RESULT=$(curl -s -F "file=@$TEST_DIR/send/test.txt" "http://127.0.0.1:$PORT/api/upload")
if echo "$UPLOAD_RESULT" | grep -q '"status":"success"'; then
    test_pass "文件上传成功"
else
    test_fail "文件上传失败"
fi

if [ -f "$TEST_DIR/recv/test.txt" ]; then
    test_pass "文件保存成功"
else
    test_fail "文件未保存"
fi

kill $PID 2>/dev/null || true
sleep 1

# 测试3: 大文件传输
echo ""
echo "Test 3: 大文件传输 (1MB)"
$SERVER_BIN -p $PORT send "$TEST_DIR/send/large.bin" &
PID=$!
sleep 2

curl -s "http://127.0.0.1:$PORT/api/download" -o "$TEST_DIR/download/large.bin"

ORIG_SIZE=$(stat -f%z "$TEST_DIR/send/large.bin" 2>/dev/null || stat -c%s "$TEST_DIR/send/large.bin")
DOWN_SIZE=$(stat -f%z "$TEST_DIR/download/large.bin" 2>/dev/null || stat -c%s "$TEST_DIR/download/large.bin")

if [ "$ORIG_SIZE" -eq "$DOWN_SIZE" ]; then
    test_pass "大文件传输成功 ($ORIG_SIZE bytes)"
else
    test_fail "大文件大小不匹配 ($ORIG_SIZE vs $DOWN_SIZE)"
fi

kill $PID 2>/dev/null || true

# 清理
cleanup

# 结果
echo ""
echo "=========================================="
echo "测试结果: $PASS 通过, $FAIL 失败"
echo "=========================================="

if [ $FAIL -eq 0 ]; then
    echo "🎉 所有测试通过!"
    exit 0
else
    exit 1
fi

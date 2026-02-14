本项目是一个局域网传输工具【玩具】

支持文件、文件夹收发。

绿色跨平台单文件，仅服务端启动即可。可通过浏览器、命令行（如curl）收发。

服务端发
```
# 发送文件
fileshare-server send test_file.txt

# 发送文件夹
fileshare-server send test_download
```

对端收
```
#接收文件/文件夹
curl -O -J "http://127.0.0.1:51809/api/download"
```

服务端收
```
fileshare-server recv test_download/
```

对端发
```
curl -F "file=@1_preview.txt" "http://127.0.0.1:51693/api/upload"
```



注意！！！

- 本项目完全由AI生成（除了README, prompt.txt）
- 网络传输安全无保障（直接走http）

本次主要是测试kimi-2.5到底怎么样，订阅的Andante，刚好用完了5小时的额度：

<img width="1809" height="270" alt="image" src="https://github.com/user-attachments/assets/d4785083-6c41-40e4-8951-58d08eeab78e" />

提示词见prompt.txt，期间对话调整了下计划（主要是安全相关、编程语言选择）

plan.md执行完后，我要求它做单元测试和黑盒测试，这个阶段花了比较长时间。

最后，功能我简单测了下都正常。界面智能说能用，但是很不好，后面再加上专门的skill看看：

<img width="921" height="1176" alt="image" src="https://github.com/user-attachments/assets/2177ac79-b547-4c7c-897b-b5dfe932b7fd" />

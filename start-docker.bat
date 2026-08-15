@echo off  
setlocal  
chcp 65001 >nul  
cd /d "%~dp0"  
  
REM --- 1. 检查 .env 是否存在，不存在则从模板复制并提示 ---  
if not exist ".env" (  
    echo [!] 未找到 .env，正在从 .env.example 复制...  
    copy ".env.example" ".env" >nul  
    echo [!] 请先编辑 .env 填写 WALLET_MNEMONIC / API_HMAC_SECRET / SIGN_TOKEN / 各地址，然后重新运行本脚本。  
    pause  
    exit /b 1  
)  
  
REM --- 2. 检查 Docker 是否已启动 ---  
docker info >nul 2>&1  
if errorlevel 1 (  
    echo [x] Docker 未运行，请先启动 Docker Desktop。  
    pause  
    exit /b 1  
)  
  
REM --- 3. 先启动基础设施：MySQL、Redis、RocketMQ ---  
echo [*] 启动基础设施 (mysql / redis / rocketmq)...  
docker compose up -d mysql redis rocketmq-namesrv rocketmq-broker  
if errorlevel 1 goto :error  

REM --- 4. 构建应用镜像 ---
echo [*] 正在构建应用镜像 wallet:v1...
docker build -t wallet:v1 .
if errorlevel 1 goto :error


REM --- 5. 启动应用服务 ---  
echo [*] 启动应用服务 (api / scanner / withdraw / sweep / sign)...  
docker compose up -d api scanner withdraw sweep sign  
if errorlevel 1 goto :error  
  
docker save -o wallet-v1.tar wallet:v1

echo.  
echo [OK] 全部服务已启动。API 监听 http://localhost:8080  
echo      查看日志：docker compose logs -f api  
goto :eof  
  
:error  
echo [x] 启动失败，请查看上面的错误信息。  
pause  
exit /b 1
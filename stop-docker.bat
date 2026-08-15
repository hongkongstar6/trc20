@echo off  
cd /d "%~dp0"  
echo [*] 停止并移除所有容器...  
docker compose down  
pause
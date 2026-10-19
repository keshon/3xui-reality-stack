@echo off
where py >nul 2>nul
if not errorlevel 1 goto launcher
where python >nul 2>nul
if not errorlevel 1 goto python
echo Python 3.11+ is required. Install Python, then run this command again.
exit /b 1
:launcher
py -3 "%~dp0scripts\launch.py" %*
exit /b %errorlevel%
:python
python "%~dp0scripts\launch.py" %*
exit /b %errorlevel%

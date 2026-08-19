#!/usr/bin/env sh
# simple-connect 安装脚本（Linux/macOS）
# 从源码构建，安装为全局命令 simple-ssh。
# 用法：
#   ./scripts/install.sh                # 安装到 ~/.local/bin
#   INSTALL_DIR=/usr/local/bin ./scripts/install.sh   # 自定义安装目录
#
# 说明：本脚本默认使用源码构建（需要 Go 工具链）；安装后终端可直接输入
# simple-ssh 启动程序。若目录已在 PATH 中则无需额外配置。

set -e

# 默认安装目录：~/.local/bin；可用环境变量 INSTALL_DIR 覆盖
if [ -z "${INSTALL_DIR:-}" ]; then
    INSTALL_DIR="$HOME/.local/bin"
fi

# 定位项目根目录（脚本位于项目根目录下 scripts/ 子目录）
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PROJECT_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

# 检查 go 工具链
if ! command -v go >/dev/null 2>&1; then
    echo "错误：未找到 go 工具链。请先安装 Go (https://go.dev/dl/) 后重试。" >&2
    exit 1
fi

# 创建安装目录
mkdir -p "$INSTALL_DIR"

echo "==> 从源码构建 simple-ssh ..."
(cd "$PROJECT_ROOT" && go build -o "$INSTALL_DIR/simple-ssh" .)

# 检查安装目录是否在 PATH 中
case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *)
        echo "==> 提示：$INSTALL_DIR 不在当前 PATH 中。"
        echo "    可将以下行加入 shell 配置（~/.bashrc / ~/.zshrc）："
        echo "      export PATH=\"$INSTALL_DIR:\$PATH\""
        ;;
esac

echo "==> 安装完成：$INSTALL_DIR/simple-ssh"
echo "    现在可以输入 simple-ssh 启动程序。"
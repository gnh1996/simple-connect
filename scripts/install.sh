#!/usr/bin/env sh
# simple-connect 安装脚本（Linux/macOS）
# 安装为全局命令 simple-ssh。
#
# 用法：
#   ./scripts/install.sh                         # 从源码构建（需 Go 工具链）
#   ./scripts/install.sh --release               # 下载 GitHub 最新 Release 预编译二进制
#   ./scripts/install.sh --release v0.1.0        # 下载指定版本（如 v0.1.0）
#   INSTALL_DIR=/usr/local/bin ./scripts/install.sh   # 自定义安装目录
#
# 说明：默认从源码构建；--release 时从 GitHub Releases 拉取对应平台的预编译二进制，
# 无需 Go 工具链。安装后终端可直接输入 simple-ssh 启动程序。

set -e

# 仓库地址（用于 --release 下载）
REPO="gnh1996/simple-connect"
RELEASE_URL="https://github.com/${REPO}/releases/download"

# 默认安装目录：~/.local/bin；可用环境变量 INSTALL_DIR 覆盖
if [ -z "${INSTALL_DIR:-}" ]; then
    INSTALL_DIR="$HOME/.local/bin"
fi

# 解析参数
MODE="build"
TAG="latest"
for arg in "$@"; do
    case "$arg" in
        --release)
            MODE="release"
            ;;
        --release=*)
            MODE="release"
            TAG="${arg#*=}"
            ;;
        -*)
            echo "错误：未知参数 $arg" >&2
            echo "用法：$0 [--release[=TAG]]" >&2
            exit 1
            ;;
        *)
            if [ "$MODE" = "release" ]; then
                TAG="$arg"
            fi
            ;;
    esac
done

# 定位项目根目录（脚本位于项目根目录下 scripts/ 子目录）
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PROJECT_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

# 创建安装目录
mkdir -p "$INSTALL_DIR"

# 解析目标平台
detect_platform() {
    case "$(uname -s)" in
        Linux) OS="linux" ;;
        Darwin) OS="darwin" ;;
        *) echo "错误：不支持的系统 $(uname -s)。" >&2; exit 1 ;;
    esac
    case "$(uname -m)" in
        x86_64|amd64) ARCH="amd64" ;;
        arm64|aarch64) ARCH="arm64" ;;
        *) echo "错误：不支持的架构 $(uname -m)。" >&2; exit 1 ;;
    esac
}

if [ "$MODE" = "build" ]; then
    # ---- 源码构建 ----
    # 检查 go 工具链
    if ! command -v go >/dev/null 2>&1; then
        echo "错误：未找到 go 工具链。请先安装 Go (https://go.dev/dl/) 后重试，" >&2
        echo "或改用 --release 参数直接安装预编译版本。" >&2
        exit 1
    fi
    echo "==> 从源码构建 simple-ssh ..."
    (cd "$PROJECT_ROOT" && go build -o "$INSTALL_DIR/simple-ssh" .)
else
    # ---- 从 GitHub Releases 下载预编译二进制 ----
    detect_platform
    if [ "$TAG" = "latest" ]; then
        URL="https://github.com/${REPO}/releases/latest/download/simple-connect-${OS}-${ARCH}"
    else
        URL="${RELEASE_URL}/${TAG}/simple-connect-${OS}-${ARCH}"
    fi
    echo "==> 下载预编译版本（$TAG，$OS/$ARCH）..."
    echo "    $URL"
    if command -v curl >/dev/null 2>&1; then
        curl -fSL --retry 3 -o "$INSTALL_DIR/simple-ssh" "$URL"
    elif command -v wget >/dev/null 2>&1; then
        wget -O "$INSTALL_DIR/simple-ssh" "$URL"
    else
        echo "错误：未找到 curl 或 wget，无法下载。" >&2
        exit 1
    fi
    chmod +x "$INSTALL_DIR/simple-ssh"
fi

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
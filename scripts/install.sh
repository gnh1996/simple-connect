#!/usr/bin/env sh
# simple-connect 安装脚本（Linux/macOS）
# 安装为全局命令 simple-ssh。
#
# 用法：
#   ./scripts/install.sh                         # 从源码构建（需 Go 工具链）
#   ./scripts/install.sh --release               # 下载 GitHub 最新 Release 预编译二进制
#   ./scripts/install.sh --release v0.2.1        # 下载指定版本（如 v0.2.1）
#   INSTALL_DIR=/usr/local/bin ./scripts/install.sh   # 自定义安装目录
#
# 说明：默认从源码构建；--release 时从 GitHub Releases 拉取对应平台的预编译二进制，
# 无需 Go 工具链。安装后终端可直接输入 simple-ssh 启动程序。
#
# 升级：可重复执行本脚本（无需先卸载），安装为原子替换——先写安装目录下的临时文件，
# 成功后一次 rename 覆盖；下载/构建失败自动清理临时文件，现有安装不受影响。
# Linux/macOS 下可替换正在运行的二进制，但需重启 simple-ssh 才会使用新版本。

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

# 解析安装目标：若 simple-ssh 是符号链接，则替换链接指向的真实文件（保持原有语义），
# 而非把链接本身换成普通文件。
DEST="$INSTALL_DIR/simple-ssh"
LINK_DEPTH=0
while [ -L "$DEST" ] && [ "$LINK_DEPTH" -lt 40 ]; do
    LINK=$(readlink "$DEST")
    case "$LINK" in
        /*) DEST="$LINK" ;;
        *) DEST="$(dirname -- "$DEST")/$LINK" ;;
    esac
    LINK_DEPTH=$((LINK_DEPTH + 1))
done
DEST_DIR=$(dirname -- "$DEST")
mkdir -p "$DEST_DIR"

# 原子替换用临时文件：与目标同目录，保证 mv 是 rename（同文件系统）。
# 任何中途失败（含 Ctrl+C / 信号）都由 EXIT trap 清理，旧版本保持原样。
TMP=""
cleanup() {
    if [ -n "$TMP" ]; then
        rm -f "$TMP"
    fi
}
trap cleanup EXIT

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
    TMP=$(mktemp "$DEST_DIR/.simple-ssh.XXXXXX")
    (cd "$PROJECT_ROOT" && go build -o "$TMP" .)
    chmod 0755 "$TMP"
    mv -f "$TMP" "$DEST"
    TMP=""
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
    TMP=$(mktemp "$DEST_DIR/.simple-ssh.XXXXXX")
    if command -v curl >/dev/null 2>&1; then
        curl -fSL --retry 3 -o "$TMP" "$URL"
    elif command -v wget >/dev/null 2>&1; then
        wget -O "$TMP" "$URL"
    else
        echo "错误：未找到 curl 或 wget，无法下载。" >&2
        exit 1
    fi
    chmod 0755 "$TMP"
    mv -f "$TMP" "$DEST"
    TMP=""
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

echo "==> 安装完成：$INSTALL_DIR/simple-ssh（原子替换）"
echo "    可重复执行本脚本升级/降级；若程序正在运行，重启后生效。"

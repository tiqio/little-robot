#!/usr/bin/env python3
"""查看 little-robot 的运行链路（.claw/traces/*.json）。

用法：
    python3 scripts/trace.py                  # 看最近一次
    python3 scripts/trace.py <trace.json>     # 看指定文件
    python3 scripts/trace.py --no-attrs       # 不打印属性
    python3 scripts/trace.py --agg            # 按 span 名称聚合统计
    python3 scripts/trace.py --times          # 额外打印每个节点的起止时间

输出里的 ∥ 表示该节点与同层其他节点并发执行。
只依赖标准库。
"""

import argparse
import glob
import json
import os
import sys
from datetime import datetime

DEFAULT_DIR = ".claw/traces"
PARALLEL = "∥"


def latest_trace(directory: str) -> str:
    files = sorted(glob.glob(os.path.join(directory, "*.json")))
    if not files:
        sys.exit(f"在 {directory} 下没找到 trace 文件，先跑一次 go run ./cmd/robot")
    return files[-1]


def parse_time(s: str) -> datetime:
    # Go 的 time.Time 序列化成 RFC3339，小数秒可能是 1~9 位；
    # 而 Python 3.9 的 fromisoformat 只认 3 位或 6 位，所以要补零/截断
    if "." in s:
        head, rest = s.split(".", 1)
        i = 0
        while i < len(rest) and rest[i].isdigit():
            i += 1
        frac, tz = rest[:i], rest[i:]
        frac = (frac + "000000")[:6]
        s = f"{head}.{frac}{tz}"
    return datetime.fromisoformat(s)


def fmt_time(s: str) -> str:
    return parse_time(s).strftime("%H:%M:%S.%f")[:-3]


def fmt_attrs(attrs: dict) -> str:
    if not attrs:
        return ""
    parts = []
    for k in sorted(attrs):
        v = attrs[k]
        if isinstance(v, bool):
            v = "true" if v else "false"
        parts.append(f"{k}={v}")
    return "  {" + ", ".join(parts) + "}"


def overlaps(a, b) -> bool:
    """两个区间是否有重叠（端点相等不算重叠）。"""
    a0, a1 = parse_time(a["start_time"]), parse_time(a["end_time"])
    b0, b1 = parse_time(b["start_time"]), parse_time(b["end_time"])
    return a0 < b1 and b0 < a1


def mark_parallel(children):
    """给与同层其他节点发生时间重叠的节点打标记，返回被标记对象的 id 集合。"""
    marked = set()
    for i in range(len(children)):
        for j in range(i + 1, len(children)):
            if overlaps(children[i], children[j]):
                marked.add(id(children[i]))
                marked.add(id(children[j]))
    return marked


def walk(node, depth, show_attrs, show_times, problems):
    name = node.get("name", "?")
    ms = node.get("duration_ms", 0)

    suffix = node.get("_mark", "")
    if suffix:
        suffix = " " + suffix

    line = f"{'  ' * depth}{name:<22}{ms:>7}ms{suffix}"
    if show_times:
        line += f"  {fmt_time(node['start_time'])} → {fmt_time(node['end_time'])}"
    if show_attrs:
        line += fmt_attrs(node.get("attrs") or {})
    print(line)

    children = sorted(node.get("children") or [], key=lambda x: x["start_time"])

    # 子节点必须落在父节点的区间内，否则说明 ctx 传错了（挂到了别的父节点上）
    p_start, p_end = parse_time(node["start_time"]), parse_time(node["end_time"])
    for c in children:
        if parse_time(c["start_time"]) < p_start or parse_time(c["end_time"]) > p_end:
            problems.append(f"{c.get('name')} 不在父节点 {name} 的时间区间内")

    marked = mark_parallel(children)
    for c in children:
        c["_mark"] = PARALLEL if id(c) in marked else ""
        walk(c, depth + 1, show_attrs, show_times, problems)


def collect(node, out):
    out.append(node)
    for c in node.get("children") or []:
        collect(c, out)


def aggregate(nodes):
    stats = {}
    for n in nodes:
        s = stats.setdefault(n.get("name", "?"), {"count": 0, "total": 0, "max": 0})
        ms = n.get("duration_ms", 0)
        s["count"] += 1
        s["total"] += ms
        s["max"] = max(s["max"], ms)
    return stats


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("path", nargs="?", help="trace 文件路径，缺省取最新一个")
    ap.add_argument("--no-attrs", action="store_true", help="不打印属性")
    ap.add_argument("--agg", action="store_true", help="按 span 名称聚合")
    ap.add_argument("--times", action="store_true", help="打印每个节点的起止时间")
    args = ap.parse_args()

    path = args.path or latest_trace(DEFAULT_DIR)
    with open(path, encoding="utf-8") as f:
        root = json.load(f)

    print(f"文件：{path}")
    print(f"图例：{PARALLEL} = 与同层其他节点并发执行\n")

    problems = []
    walk(root, 0, not args.no_attrs, args.times, problems)

    nodes = []
    collect(root, nodes)

    child_sum = sum(c.get("duration_ms", 0) for c in root.get("children") or [])
    print()
    print(f"总计 {root.get('duration_ms', 0)}ms ｜ 一级子节点之和 {child_sum}ms ｜ span 数 {len(nodes)}")
    if child_sum > root.get("duration_ms", 0):
        print(f"（子节点之和 > 父节点，因为有 {PARALLEL} 并发执行，属正常现象）")

    if args.agg:
        print("\n按名称聚合：")
        print(f"  {'name':<22}{'次数':>5}{'总耗时':>10}{'最大':>10}")
        for name, s in sorted(aggregate(nodes).items(), key=lambda kv: -kv[1]["total"]):
            print(f"  {name:<22}{s['count']:>5}{s['total']:>9}ms{s['max']:>9}ms")

    if problems:
        print("\n⚠️ 结构异常：")
        for p in problems:
            print("  - " + p)
    else:
        print("\n结构检查：所有子节点都在父节点时间区间内 ✅")


if __name__ == "__main__":
    main()

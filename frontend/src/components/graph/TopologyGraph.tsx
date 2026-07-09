import { FC, useEffect, useRef } from 'react';
import Graph from 'graphology';
import Sigma from 'sigma';

export interface GraphNode {
  id: string;
  label: string;
  x?: number;
  y?: number;
  size?: number;
  color?: string;
}

export interface GraphEdge {
  source: string;
  target: string;
  label?: string;
}

export interface TopologyGraphProps {
  nodes: GraphNode[];
  edges: GraphEdge[];
  className?: string;
}

/**
 * TopologyGraph renders an interactive graph of CI relationships
 * using Sigma.js backed by a graphology instance.
 */
export const TopologyGraph: FC<TopologyGraphProps> = ({ nodes, edges, className }) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const sigmaRef = useRef<Sigma | null>(null);

  useEffect(() => {
    if (!containerRef.current) return;

    const graph = new Graph();

    nodes.forEach((node, i) => {
      graph.addNode(node.id, {
        label: node.label,
        x: node.x ?? Math.cos((2 * Math.PI * i) / nodes.length),
        y: node.y ?? Math.sin((2 * Math.PI * i) / nodes.length),
        size: node.size ?? 10,
        color: node.color ?? '#4f46e5',
      });
    });

    edges.forEach((edge) => {
      if (graph.hasNode(edge.source) && graph.hasNode(edge.target)) {
        graph.addEdge(edge.source, edge.target, { label: edge.label });
      }
    });

    const renderer = new Sigma(graph, containerRef.current, {
      renderEdgeLabels: true,
    });

    sigmaRef.current = renderer;

    return () => {
      renderer.kill();
      sigmaRef.current = null;
    };
  }, [nodes, edges]);

  return <div ref={containerRef} className={className} style={{ width: '100%', height: '100%' }} />;
};
